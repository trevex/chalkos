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
	if !reflect.DeepEqual(resp.Msg.Schemas, fixtureSchemas(t)) || len(resp.Msg.KubernetesCluster) != 0 {
		t.Errorf("contract = %d schemas and kubernetes.json %q, want the image's schemas alone", len(resp.Msg.Schemas), resp.Msg.KubernetesCluster)
	}
	if m := resp.Msg; m.Cluster != "lab" || m.Role != "worker" || m.Platform != "metal" {
		t.Errorf("contract of the image of %q, %q and %q, want the os-release's lab, worker and metal", m.Cluster, m.Role, m.Platform)
	}
	write(t, s.Paths.KubernetesCluster, workerCluster)
	if resp, err = s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{})); err != nil || string(resp.Msg.KubernetesCluster) != workerCluster {
		t.Errorf("kubernetes.json = %q, %v", resp.Msg.GetKubernetesCluster(), err)
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
