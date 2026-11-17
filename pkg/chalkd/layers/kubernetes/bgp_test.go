package kubernetes

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	frrstatus "github.com/trevex/chalkos/pkg/frr/status"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

func TestLocalBGPStatus(t *testing.T) {
	root := t.TempDir()
	link, speakers := filepath.Join(root, "frr.conf"), filepath.Join(root, "BGPSpeaker")
	if err := os.Mkdir(speakers, 0700); err != nil {
		t.Fatal(err)
	}
	speaker := filepath.Join(speakers, "fabric.json")
	writeSpeaker := func() {
		t.Helper()
		if err := os.WriteFile(speaker, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSpeaker()
	query := "vtysh -c " + frrstatus.NeighborsCommand
	run := &tooltest.Runner{}
	if got := localBGPStatus(t.Context(), run, link, speakers); got != nil {
		t.Fatalf("disabled BGP returned %v", got)
	}
	if err := os.Symlink(filepath.Join(root, "not-written-yet"), link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(speaker); err != nil {
		t.Fatal(err)
	}
	if got := localBGPStatus(t.Context(), run, link, speakers); got != nil {
		t.Fatalf("unconfigured BGP returned %v", got)
	}
	if len(run.Calls) != 0 {
		t.Fatalf("FRR queried without a configured speaker: %v", run.Calls)
	}
	writeSpeaker()
	run.Rules = []tooltest.Rule{{Prefix: query, Out: `{"default":{"fd00::1":{"peerGroup":"peer-router","bgpState":"Active"}}}`}}
	got := localBGPStatus(t.Context(), run, link, speakers)
	if len(got.GetPeers()) != 1 || got.Peers[0].State != "Active" || got.Peers[0].Name != "router" {
		t.Fatalf("configured BGP returned %v", got)
	}
	run.Rules = []tooltest.Rule{{Prefix: query, Out: "{}"}}
	if got := localBGPStatus(t.Context(), run, link, speakers); got == nil || got.Error != "" || len(got.Peers) != 0 {
		t.Fatalf("BGP without sessions returned %v", got)
	}
	run.Rules = []tooltest.Rule{{Prefix: query, Err: errors.New("failed to connect to any daemons")}}
	if got := localBGPStatus(t.Context(), run, link, speakers); !strings.Contains(got.GetError(), "failed to connect") {
		t.Fatalf("offline FRR returned %v", got)
	}
}
