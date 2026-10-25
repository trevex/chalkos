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
	for _, l := range []layers.Set{nil, layers.Of(layers.Kubernetes)} {
		s, _ := newTestServer(t, normal, vda)
		s.Layers = l
		info, err := s.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		contract, err := s.Contract(context.Background(), connect.NewRequest(&nodev1.ContractRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if want := l.Names(); !slices.Equal(info.Msg.Layers, want) || !slices.Equal(contract.Msg.Layers, want) {
			t.Errorf("layers %q in Info and %q in Contract, want %q", info.Msg.Layers, contract.Msg.Layers, want)
		}
	}
}
