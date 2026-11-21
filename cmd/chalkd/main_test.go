package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/layer/layertest"
	"github.com/trevex/chalkos/pkg/pki"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCredentials(t *testing.T) {
	now := time.Now()
	ca, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(ca, now)
	if err != nil {
		t.Fatal(err)
	}
	nodeCert, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, now)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("maintenance on a generic image", func(t *testing.T) {
		root := t.TempDir()
		c, err := loadCredentials(filepath.Join(root, "state"), filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now)
		if err != nil {
			t.Fatal(err)
		}
		if c.mode != nodev1.Mode_MODE_MAINTENANCE || c.clientCAs != nil {
			t.Errorf("credentials = %+v, want maintenance accepting any client", c)
		}
		if _, err := os.Stat(filepath.Join(root, "run", "maintenance.crt")); err != nil {
			t.Error("the maintenance certificate was not written")
		}
	})
	t.Run("maintenance on an image with an OS CA", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "os-ca.crt"), ca.Certificate)
		c, err := loadCredentials(filepath.Join(root, "state"), filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now)
		if err != nil {
			t.Fatal(err)
		}
		if c.mode != nodev1.Mode_MODE_MAINTENANCE || c.clientCAs == nil {
			t.Errorf("credentials = %+v, want maintenance requiring clients of the OS CA", c)
		}
	})
	for name, bundle := range map[string]string{
		"junk after the OS CA": ca.Certificate + "junk\n",
		"a node CA":            nodeCA.Certificate,
		"a node certificate":   nodeCert.Certificate,
	} {
		t.Run("maintenance refuses an image carrying "+name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "os-ca.crt"), bundle)
			if _, err := loadCredentials(filepath.Join(root, "state"), filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now); err == nil {
				t.Error("the bundle was accepted as the OS CA")
			}
		})
	}
	t.Run("normal", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		writeFile(t, filepath.Join(state, "installed"), "")
		writeFile(t, filepath.Join(state, "chalkd", "node.pem"), nodeCert.Certificate+nodeCert.Key)
		writeFile(t, filepath.Join(state, "chalkd", "ca.crt"), ca.Certificate)
		c, err := loadCredentials(state, filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now)
		if err != nil {
			t.Fatal(err)
		}
		if c.mode != nodev1.Mode_MODE_NORMAL || c.clientCAs == nil {
			t.Errorf("credentials = %+v, want normal mode", c)
		}
		if fp, err := c.fingerprint(); err != nil || fp != pki.Fingerprint(c.node.Current().Leaf.Raw) {
			t.Errorf("fingerprint = %q, %v", fp, err)
		}
	})
	t.Run("installed without its certificate", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		writeFile(t, filepath.Join(state, "installed"), "")
		if _, err := loadCredentials(state, filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now); err == nil {
			t.Error("an installed node without its certificate fell back to maintenance mode")
		}
	})
	t.Run("unreadable state fails closed", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		if err := os.MkdirAll(state, 0o755); err != nil {
			t.Fatal(err)
		}
		// A state directory without execute permission makes Stat of a file inside it fail with
		// something other than "does not exist", which must not be treated as "not installed".
		if err := os.Chmod(state, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(state, 0o755)
		if _, err := loadCredentials(state, filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now); err == nil {
			t.Error("an unreadable state directory fell back to maintenance mode instead of failing")
		}
	})
}

func TestMaintenanceCertificateAcrossRestarts(t *testing.T) {
	now := time.Now()
	load := func(t *testing.T, run string, now time.Time) []byte {
		t.Helper()
		root := filepath.Dir(run)
		c, err := loadCredentials(filepath.Join(root, "state"), filepath.Join(root, "os-ca.crt"), run, now)
		if err != nil {
			t.Fatal(err)
		}
		return c.maintenance.Certificate[0]
	}

	t.Run("reused", func(t *testing.T) {
		run := filepath.Join(t.TempDir(), "run")
		first := load(t, run, now)
		if second := load(t, run, now.Add(time.Hour)); !bytes.Equal(first, second) {
			t.Error("a restart changed the maintenance certificate")
		}
		for path, want := range map[string]os.FileMode{run: 0o700, filepath.Join(run, "maintenance.key"): 0o600} {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s has mode %v, want %v", path, fi.Mode().Perm(), want)
			}
		}
	})
	for name, damage := range map[string]func(run string) error{
		"key missing":         func(run string) error { return os.Remove(filepath.Join(run, "maintenance.key")) },
		"certificate missing": func(run string) error { return os.Remove(filepath.Join(run, "maintenance.crt")) },
		"key corrupt": func(run string) error {
			return os.WriteFile(filepath.Join(run, "maintenance.key"), []byte("-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n"), 0o600)
		},
		"certificate corrupt": func(run string) error {
			return os.WriteFile(filepath.Join(run, "maintenance.crt"), []byte("garbage"), 0o644)
		},
	} {
		t.Run("regenerated when "+name, func(t *testing.T) {
			run := filepath.Join(t.TempDir(), "run")
			first := load(t, run, now)
			if err := damage(run); err != nil {
				t.Fatal(err)
			}
			second := load(t, run, now)
			if bytes.Equal(first, second) {
				t.Fatal("kept a maintenance certificate whose files are damaged")
			}
			// The new pair is on disk and is reused in turn.
			if third := load(t, run, now); !bytes.Equal(second, third) {
				t.Error("the regenerated certificate was not kept")
			}
		})
	}
	t.Run("regenerated when expired", func(t *testing.T) {
		run := filepath.Join(t.TempDir(), "run")
		first := load(t, run, now)
		if second := load(t, run, now.Add(pki.LeafValidity+time.Hour)); bytes.Equal(first, second) {
			t.Error("kept an expired maintenance certificate")
		}
	})
}

func TestHealthArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want healthArgs
		ok   bool
	}{
		{nil, healthArgs{}, true},
		{[]string{"--wait", "300"}, healthArgs{wait: 300 * time.Second}, true},
		{[]string{"--wait", "300", "--ignore-unit", "a.service", "--ignore-unit", "b.timer"}, healthArgs{wait: 300 * time.Second, ignore: []string{"a.service", "b.timer"}}, true},
		{[]string{"--stopped"}, healthArgs{stopped: true}, true},
		{[]string{"--wait", "0"}, healthArgs{}, false},
		{[]string{"--wait", "x"}, healthArgs{}, false},
		{[]string{"--wait", "300", "--stopped"}, healthArgs{}, false},
		{[]string{"now"}, healthArgs{}, false},
	} {
		var got *healthArgs
		cmd := healthCommand(func(a healthArgs) error {
			got = &a
			return nil
		})
		cmd.SetArgs(tc.args)
		cmd.SetErr(io.Discard)
		cmd.SetOut(io.Discard)
		err := cmd.Execute()
		if (err == nil) != tc.ok || tc.ok && (got == nil || got.wait != tc.want.wait || got.stopped != tc.want.stopped || !slices.Equal(got.ignore, tc.want.ignore)) {
			t.Errorf("health %q ran with %+v, %v", tc.args, got, err)
		}
	}
}

// TestCommandsRefuseArguments refuses, before running anything, the arguments a command does not
// take, and a command chalkd does not have, with a usage error.
func TestCommandsRefuseArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"serve", "now"},
		{"load-config", "x"},
		{"check"},
		{"check", "--system", "/nix/store/x"},
		{"check", "--system", "", "--facts", ""},
		{"check", "--system", "/nix/store/x", "--facts", "/run/f.sock", "now"},
		{"prepare-kubernetes"},
		{"prepare"},
		{"kubernetes"},
		{"kubernetes", "prepare", "--vxlan"},
		{"router", "prepare"},
		{"router", "services", "bind"},
		{"apply-links", "eth0"},
		{"chrony-dhcp"},
		{"chrony-reload-sources", "now"},
		{"tcp-connect", "192.0.2.1"},
		{"frr-credentials", "/run/chalkos/outputs/frr/frr.conf"},
		{"router", "nat64", "start"},
		{"router", "prune-devices", "guest"},
		{"router", "record-device", "kea-dhcp4"},
		{"router", "resolver-vrfs-ready", "guest"},
		{"router", "load-firewall", "/run/ruleset.nft"},
		{"router", "dhcp6-pd"},
		{"router", "ntp-clients", "now"},
		{"router", "nat64-sessions"},
		{"router", "dhcp-leases", "office"},
	} {
		root, err := newCommand()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		root.SetArgs(args)
		root.SetErr(&stderr)
		if status := chalkd.Execute(root); status != 2 || !strings.Contains(stderr.String(), "Usage:") {
			t.Errorf("chalkd %q exits with %d:\n%s", args, status, stderr.String())
		}
	}
}

// TestCallSites takes each command line the image's units and the kubelet run, as they run it.
func TestCallSites(t *testing.T) {
	for _, args := range [][]string{
		{"serve"},
		{"load-config"},
		{"health", "--wait", "30"},
		{"health", "--wait", "30", "--ignore-unit", "a.service", "--ignore-unit", "b.service"},
		{"health", "--stopped"},
		{"check", "--system", "/nix/store/x-system", "--facts", "/run/facts.sock"},
		{"apply-links"},
		{"chrony-dhcp", "/run/chalkos/chrony-dhcp/dhcp.sources"},
		{"chrony-reload-sources"},
		{"tcp-connect", "2001:db8::1", "443"},
		{"frr-credentials", "/run/chalkos/outputs/frr/frr.conf", "/nix/store/x-frr-fallback.conf"},
		{"kubernetes", "prepare"},
		{"kubernetes", "prepare", "--vxlan-rule"},
		{"kubernetes", "vxlan-rule"},
		{"kubernetes", "controlplane"},
		{"kubernetes", "controlplane", "--firewall-mark", "0x02000000"},
		{"kubernetes", "registry-mirrors"},
		{"kubernetes", "containerd-config", "/run/chalkos/containerd/cluster.toml"},
		{"kubernetes", "registry-credentials"},
		{"kubernetes", "credential-provider"},
		{"router", "dhcp6-routes"},
		{"router", "services", "kea"},
		{"router", "services", "kea", "stop"},
		{"router", "services", "unbound"},
		{"router", "services", "relay", "stop"},
		{"router", "nat64"},
		{"router", "nat64", "stop"},
		{"router", "prune-devices"},
		{"router", "record-device", "kea-dhcp4", "access"},
		{"router", "resolver-vrfs-ready"},
		{"router", "load-firewall", "/run/chalkos/outputs/router-nftables/ruleset.nft", "/nix/store/x-fallback.nft"},
		{"router", "dhcp6-pd", "leases6_committed"},
		{"router", "ntp-clients", "--chronyc", "/nix/store/x-chrony/bin/chronyc"},
		{"router", "nat64-sessions", "--jool", "/nix/store/x-jool/bin/jool", "edge"},
		{"router", "dhcp-leases", "--subnet=", "--mac="},
		{"router", "dhcp-leases", "--subnet=office", "--mac=52:54:00:12:34:56"},
	} {
		root, err := newCommand()
		if err != nil {
			t.Fatal(err)
		}
		cmd, rest, err := root.Find(args)
		if err != nil || cmd.HasSubCommands() || layertest.Refused(cmd, rest) {
			t.Errorf("chalkd %q is refused as %s, %v", args, cmd.CommandPath(), err)
		}
	}
}

// A link chalkd-<layer>-<command> runs chalkd <layer> <command> with the link's arguments, as Kea's
// run_script hook runs chalkd-router-dhcp6-pd; chalkd and other names run chalkd's own command line.
func TestLinkedCommand(t *testing.T) {
	args, ok := linkedCommand([]string{"/nix/store/x-chalkos-dhcp6-pd/bin/chalkd-router-dhcp6-pd", "leases6_committed"})
	if want := []string{"router", "dhcp6-pd", "leases6_committed"}; !ok || !slices.Equal(args, want) {
		t.Errorf("linked command = %q, %v, want %q", args, ok, want)
	}
	root, err := newCommand()
	if err != nil {
		t.Fatal(err)
	}
	if cmd, rest, err := root.Find(args); err != nil || cmd.CommandPath() != "chalkd router dhcp6-pd" || layertest.Refused(cmd, rest) {
		t.Errorf("chalkd %q is refused as %s, %v", args, cmd.CommandPath(), err)
	}
	for _, argv0 := range []string{"/nix/store/x-chalkd/bin/chalkd", "chalkd", "chalkd-router"} {
		if args, ok := linkedCommand([]string{argv0, "serve"}); ok {
			t.Errorf("%s runs %q", argv0, args)
		}
	}
}

// chalkd's unit passes the time a counted boot has to become healthy in seconds, after which
// Status reports a boot settled.
func TestHealthTimeout(t *testing.T) {
	for value, want := range map[string]time.Duration{"": 0, "300": 5 * time.Minute, "0": 0} {
		if got, err := healthTimeout(func(string) string { return value }); err != nil || got != want {
			t.Errorf("%q: %v, %v, want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"5m", "-1"} {
		if _, err := healthTimeout(func(string) string { return value }); err == nil {
			t.Errorf("%q: accepted", value)
		}
	}
}
