package chalkd

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
)

func TestContract(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	resp, err := s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resp.Msg.Schemas, fixtureSchemas(t)) || len(resp.Msg.LayerFacts) != 0 {
		t.Errorf("contract = %d schemas and %d layer facts, want the image's schemas alone", len(resp.Msg.Schemas), len(resp.Msg.LayerFacts))
	}
	if m := resp.Msg; m.Variant != "worker" || m.Platform != "metal" {
		t.Errorf("contract of the image of %q and %q, want the os-release's worker and metal", m.Variant, m.Platform)
	}
	kubernetesImage(t, s)
	if resp, err = s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{})); err != nil || len(resp.Msg.LayerFacts) != 1 {
		t.Fatalf("layer facts = %v, %v; want the image.json", resp.Msg.GetLayerFacts(), err)
	}
	var facts kubernetesv1.ImageFacts
	if err := resp.Msg.LayerFacts[0].UnmarshalTo(&facts); err != nil || string(facts.ImageJson) != kubernetestest.Image {
		t.Errorf("image.json = %q, %v", facts.ImageJson, err)
	}
	s.Loader.Schemas = filepath.Join(t.TempDir(), "missing")
	if _, err := s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("an image without schemas: %v, want failed_precondition", err)
	}
	if _, _, err := s.runningTarget(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("translating for an image without schemas: %v, want failed_precondition", err)
	}
}

// TestContractRefusedOnTheInstaller refuses the installer's own schemas, which are not those of
// the image it installs.
func TestContractRefusedOnTheInstaller(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	_, err := s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "take the schemas from the image you install") {
		t.Errorf("err = %v", err)
	}
}
