package chalkd

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/pki"
)

// Info and Contract name the image's layers in order, none for an image of the base alone.
func TestInfoAndContractNameTheLayers(t *testing.T) {
	for _, c := range []struct {
		layers layers.Set
		want   []string
	}{
		{nil, nil},
		{layers.Of(layers.Kubernetes), []string{"kubernetes"}},
	} {
		s, _ := newTestServer(t, normal, vda)
		s.Layers = c.layers
		info, err := s.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		contract, err := s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(info.Msg.Layers, c.want) || !slices.Equal(contract.Msg.Layers, c.want) {
			t.Errorf("layers %q in Info and %q in Contract, want %q", info.Msg.Layers, contract.Msg.Layers, c.want)
		}
	}
}

// kubernetesRPCs are the RPCs of the Kubernetes layer.
var kubernetesRPCs = []string{"Bootstrap", "EtcdMembers", "EtcdRemoveMember", "EtcdLeave", "RotationStep", "DrainNode", "UncordonNode", "RevokeNode", "UnrevokeNode"}

// An RPC of a layer the image lacks is unimplemented in either mode, before the mode and the role
// are checked; on an image with the layer, the mode and the role decide as before.
func TestLayerGate(t *testing.T) {
	c := newCreds(t)
	for _, mode := range []nodev1.Mode{normal, maintenance} {
		s, _ := newTestServer(t, mode, vda)
		conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleAdmin])
		for _, rpc := range kubernetesRPCs {
			if got := call(conn, rpc); got != connect.CodeUnimplemented {
				t.Errorf("%s in %v without the Kubernetes layer: code %v, want unimplemented", rpc, mode, got)
			}
		}
		_, err := conn.Bootstrap(context.Background(), connect.NewRequest(&nodev1.BootstrapRequest{}))
		if want := "the kubernetes layer is not in this image"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("bootstrap in %v: %v, want %q", mode, err, want)
		}
		_, err = conn.Routes(context.Background(), connect.NewRequest(&nodev1.RoutesRequest{}))
		if want := "the router layer is not in this image"; connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), want) {
			t.Errorf("routes in %v without the router layer: %v, want %q", mode, err, want)
		}
	}
	worker, _ := kubernetesServer(t, k8s.KindWorker, true)
	addr := serve(t, worker, c, c.pool)
	if got := call(dial(t, addr, c.clients[pki.RoleReader]), "Bootstrap"); got != connect.CodePermissionDenied {
		t.Errorf("bootstrap as a reader with the layer: code %v, want permission denied", got)
	}
	if got := call(dial(t, addr, c.clients[pki.RoleAdmin]), "EtcdMembers"); got != connect.CodeFailedPrecondition {
		t.Errorf("etcd members on a worker with the layer: code %v, want failed precondition", got)
	}
}
