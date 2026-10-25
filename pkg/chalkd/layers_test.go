package chalkd

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/layers"
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
