package chalkd

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
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
		withPlugins(t, s, c.layers, nil)
		if c.layers.Has(layers.Kubernetes) {
			// The Kubernetes layer reads its image.json.
			kubernetesImage(t, s)
		}
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
var kubernetesRPCs = []string{"KubernetesStatus", "Bootstrap", "EtcdMembers", "EtcdRemoveMember", "EtcdLeave", "RotationStep", "DrainNode", "UncordonNode", "RevokeNode", "UnrevokeNode"}

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
		if got := call(conn, "RouterStatus"); got != connect.CodeUnimplemented {
			t.Errorf("router status in %v without the router layer: code %v, want unimplemented", mode, got)
		}
		k := kubernetesv1connect.NewKubernetesServiceClient(conn.HTTPClient(), conn.BaseURL())
		_, err := k.Bootstrap(context.Background(), connect.NewRequest(&kubernetesv1.BootstrapRequest{}))
		if want := "the kubernetes layer is not in this image"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("bootstrap in %v: %v, want %q", mode, err, want)
		}
		r := routerv1connect.NewRouterServiceClient(conn.HTTPClient(), conn.BaseURL())
		_, err = r.Routes(context.Background(), connect.NewRequest(&routerv1.RoutesRequest{}))
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
