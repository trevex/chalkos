package chalkd

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

func TestContract(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	resp, err := s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resp.Msg.Schemas, fixtureSchemas(t)) || len(resp.Msg.KubernetesImage) != 0 {
		t.Errorf("contract = %d schemas and image.json %q, want the image's schemas alone", len(resp.Msg.Schemas), resp.Msg.KubernetesImage)
	}
	if m := resp.Msg; m.Variant != "worker" || m.Platform != "metal" {
		t.Errorf("contract of the image of %q and %q, want the os-release's worker and metal", m.Variant, m.Platform)
	}
	write(t, s.Identity.KubernetesImage, testImage)
	if resp, err = s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{})); err != nil || string(resp.Msg.KubernetesImage) != testImage {
		t.Errorf("image.json = %q, %v", resp.Msg.GetKubernetesImage(), err)
	}
	s.Identity.Schemas = filepath.Join(t.TempDir(), "missing")
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
