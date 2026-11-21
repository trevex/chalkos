package router

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/trevex/chalkos/pkg/internal/cli"
	routing "github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// The kind, name and table of a .netdev, with comments, spaces around keys and values, the last of
// a key winning, and keys of other sections.
func TestReadNetdev(t *testing.T) {
	for unit, want := range map[string]netdev{
		"[NetDev]\nKind=vrf\nName=keep\n\n[VRF]\nTable=100\n":                                {"vrf", "keep", "100"},
		"# A VRF.\n[NetDev]\nName = local\n\tKind\t= vrf\n[VRF]\nTable = 500":                {"vrf", "local", "500"},
		"[NetDev]\nKind=wireguard\nName=wg0\n[WireGuard]\nPrivateKeyFile=/run/k\n":           {"wireguard", "wg0", ""},
		"[NetDev]\nKind=vrf\nName=a\nName=b\n ;Table=1\n[Match]\nTable=2\n[VRF]\n#Table=3\n": {"vrf", "b", ""},
		"[NetDev] \nKind=dummy\nName=d0\n":                                                   {"dummy", "d0", ""},
		"":                                                                                   {},
		"[NetDev]\nKind=vrf\n":                                                               {"vrf", "", ""},
	} {
		got, ok := readNetdev([]byte(unit))
		if got != want || ok != ((want.kind == "vrf" || want.kind == "wireguard") && want.name != "") {
			t.Errorf("%q: %+v, %v, want %+v", unit, got, ok, want)
		}
	}
}

// prune-devices removes the VRFs no unit declares with their tables, after their unreachable
// defaults, and the WireGuard devices no unit declares; a unit of an earlier directory, or one it
// masks, wins. A failed removal fails the run after the others.
func TestPruneDevices(t *testing.T) {
	p, r := routerNode(t)
	etc, rendered := t.TempDir(), t.TempDir()
	p.NetdevDirs = []string{etc, rendered, filepath.Join(t.TempDir(), "missing")}
	touch(t, filepath.Join(rendered, "00-vrf-keep.netdev"), "[NetDev]\nKind=vrf\nName=keep\n[VRF]\nTable=100\n")
	touch(t, filepath.Join(rendered, "00-vrf-moved.netdev"), "[NetDev]\nKind=vrf\nName=moved\n[VRF]\nTable=300\n")
	touch(t, filepath.Join(rendered, "00-vrf-masked.netdev"), "[NetDev]\nKind=vrf\nName=masked\n[VRF]\nTable=600\n")
	touch(t, filepath.Join(rendered, "30-wg0.netdev"), "[NetDev]\nKind=wireguard\nName=wg0\n")
	touch(t, filepath.Join(etc, "10-local.netdev"), "[NetDev]\nName = local\nKind = vrf\n[VRF]\nTable = 500\n")
	if err := os.Symlink(os.DevNull, filepath.Join(etc, "00-vrf-masked.netdev")); err != nil {
		t.Fatal(err)
	}
	p.Links = func() ([]routing.Link, error) {
		return []routing.Link{
			{Name: "eth0"},
			{Name: "keep", Kind: "vrf", Table: 100},
			{Name: "moved", Kind: "vrf", Table: 200},
			{Name: "gone", Kind: "vrf", Table: 400},
			{Name: "local", Kind: "vrf", Table: 500},
			{Name: "masked", Kind: "vrf", Table: 600},
			{Name: "wg0", Kind: "wireguard"},
			{Name: "wg1", Kind: "wireguard"},
			{Name: "d0", Kind: "dummy"},
		}, nil
	}
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "ip -6 route flush table 400 ", Err: errors.New("exit status 2")})
	var stderr bytes.Buffer
	if err := p.pruneDevices(context.Background(), &stderr); err != cli.Status(1) {
		t.Errorf("error %v", err)
	}
	want := []string{
		"ip -4 route flush table 200 exact 0.0.0.0/0 type unreachable proto static",
		"ip -6 route flush table 200 exact ::/0 type unreachable proto static",
		"ip link delete moved",
		"ip -4 route flush table 400 exact 0.0.0.0/0 type unreachable proto static",
		"ip -6 route flush table 400 exact ::/0 type unreachable proto static",
		"ip -4 route flush table 600 exact 0.0.0.0/0 type unreachable proto static",
		"ip -6 route flush table 600 exact ::/0 type unreachable proto static",
		"ip link delete masked",
		"ip link delete wg1",
	}
	if !slices.Equal(r.Calls, want) {
		t.Errorf("calls = %q, want %q", r.Calls, want)
	}
	if got := stderr.String(); got != "chalkd router prune-devices: failed to remove the device gone\n" {
		t.Errorf("said %q", got)
	}
}

// record-device writes the index of the VRF's device to the unit's runtime directory, and fails
// without a device or a runtime directory.
func TestRecordDevice(t *testing.T) {
	p, _ := routerNode(t)
	touch(t, filepath.Join(p.NetDir, "access", "ifindex"), "7\n")
	dir := t.TempDir()
	if err := p.recordDevice("kea-dhcp4", "access", dir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "kea-dhcp4-access.ifindex")); err != nil || string(got) != "7\n" {
		t.Errorf("record = %q, %v", got, err)
	}
	if err := p.recordDevice("kea-dhcp4", "gone", dir); err == nil {
		t.Error("recorded the device of a VRF without one")
	}
	if err := p.recordDevice("kea-dhcp4", "access", ""); err == nil {
		t.Error("recorded without a runtime directory")
	}
}

// resolver-vrfs-ready holds without resolvers of VRFs and once each one's VRF has its rendered
// table, asks to wait (75) while one does not, and fails without the VRF's unit or table.
func TestResolverVRFsReady(t *testing.T) {
	links := []routing.Link{{Name: "guest", Kind: "vrf", Table: 300}, {Name: "iot", Kind: "dummy"}}
	for name, c := range map[string]struct {
		vrf, netdev string
		want        error
	}{
		"no resolver of a VRF": {"", "", nil},
		"ready":                {"guest", "[NetDev]\nKind=vrf\nName=guest\n\n[VRF]\nTable=300\n", nil},
		"another table":        {"guest", "[NetDev]\nKind=vrf\nName=guest\n\n[VRF]\nTable=301\n", cli.Status(notReady)},
		"no VRF":               {"iot", "[NetDev]\nKind=vrf\nName=iot\n\n[VRF]\nTable=400\n", cli.Status(notReady)},
		"no table":             {"guest", "[NetDev]\nKind=vrf\nName=guest\n", errors.New("")},
		"no unit":              {"guest", "", errors.New("")},
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := routerNode(t)
			p.NetworkdDir = t.TempDir()
			p.Links = func() ([]routing.Link, error) { return links, nil }
			touch(t, filepath.Join(p.ResolverDir, "unbound.conf"), "")
			if c.vrf != "" {
				touch(t, filepath.Join(p.ResolverDir, "unbound-"+c.vrf+".conf"), "")
			}
			if c.netdev != "" {
				touch(t, filepath.Join(p.NetworkdDir, "00-vrf-"+c.vrf+".netdev"), c.netdev)
			}
			err := p.resolverVRFsReady()
			switch {
			case c.want == nil && err != nil, c.want != nil && err == nil:
				t.Errorf("error %v, want %v", err, c.want)
			case errors.Is(c.want, cli.Status(notReady)) != errors.Is(err, cli.Status(notReady)):
				t.Errorf("error %v, want %v", err, c.want)
			}
		})
	}
}
