package chalkctl

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/layers"
)

// TestStatusWithABrokenConfig reports the rest of the status when chalkctl cannot read the node's
// config from the inventory.
func TestStatusWithABrokenConfig(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	ta.writeNode(t, n1SystemDisk, n1SystemDisk, n1Network)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	out := ta.stdout.String()
	if !strings.Contains(out, "config none (cannot read the config the inventory gives it: ") || !strings.Contains(out, "platform metal") || !strings.Contains(out, "var") {
		t.Errorf("status = %q", out)
	}
}

// status names each output with the start of its hash and why its last render failed, or that
// none rendered it yet.
func TestOutputLine(t *testing.T) {
	for _, tc := range []struct {
		o    *nodev1.OutputStatus
		want string
	}{
		{&nodev1.OutputStatus{Name: "bird-conf", Hash: strings.Repeat("ab", 32)}, "bird-conf\tabababababab\trendered"},
		{&nodev1.OutputStatus{Name: "bird-conf", Hash: strings.Repeat("ab", 32), Error: "its check refused the rendered files (exit 1); see the node's journal"}, "bird-conf\tabababababab\tfailed: its check refused the rendered files (exit 1); see the node's journal"},
		{&nodev1.OutputStatus{Name: "bird-conf", Error: "its renderer failed (exit 2); see the node's journal"}, "bird-conf\tnone\tfailed: its renderer failed (exit 2); see the node's journal"},
		{&nodev1.OutputStatus{Name: "bird-conf"}, "bird-conf\tnone\tnot rendered yet"},
	} {
		if got := outputLine(tc.o); got != tc.want {
			t.Errorf("outputLine(%v) = %q, want %q", tc.o, got, tc.want)
		}
	}
}

// apilessLayer is a layer whose chalkd serves no API of it, as a chalkd from before the layers had
// services of their own.
type apilessLayer string

func (l apilessLayer) Name() string { return string(l) }
func (apilessLayer) Handler(...connect.HandlerOption) (string, http.Handler) {
	return "/apiless/", http.NotFoundHandler()
}
func (apilessLayer) Permissions() map[string]layer.Permission { return nil }
func (apilessLayer) Start(context.Context) error              { return nil }

// status says to upgrade a chalkd whose Info names a layer but which serves no API of it.
func TestStatusOfAChalkdWithoutTheLayersAPI(t *testing.T) {
	for _, name := range []string{layers.Kubernetes, layers.Router} {
		t.Run(name, func(t *testing.T) {
			ta := newTestApp(t)
			current, _ := json.Marshal(ta.identity(t))
			s, _ := installedNode(t, ta, current)
			withPlugins(t, s, layers.Of(name), apilessLayer(name))
			addr := ta.startNode(t, s)
			err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr))
			if want := "the node's chalkd serves no API of the " + name + " layer; upgrade chalkd first"; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}
}

// TestBootLines names each image by its root hash, with the tag and revision the node knows.
func TestBootLines(t *testing.T) {
	running := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x81, 0xbd}, 16), Generation: 7, Revision: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Tag: "v1.4.0"}
	next := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x3f, 0xa9}, 16), Generation: 8, Tag: "v1.5.0"}
	for _, tc := range []struct {
		boot *nodev1.BootStatus
		want []string
	}{
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true}, []string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7+2-1.efi"}, []string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7+2-1.efi, not found healthy yet"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Staged: next},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) installed; it boots next"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Failed: next, Journal: []string{"a", "b"}},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) failed: rolled back to 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6)", "  a", "  b"}},
		{&nodev1.BootStatus{Running: &nodev1.BootImage{RootHash: next.RootHash}, Error: "no UKI on the ESP boots the running store"}, []string{"image 3fa93fa93fa9; its boot is unknown: no UKI on the ESP boots the running store"}},
		{nil, nil},
	} {
		if got := bootLines(tc.boot); !slices.Equal(got, tc.want) {
			t.Errorf("bootLines(%v) = %q, want %q", tc.boot, got, tc.want)
		}
	}
}
