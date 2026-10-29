package chalkctl

import (
	"context"
	"encoding/json"
	"net/http"
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
