package layer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// stringPayloads is a Delivery that takes string payloads and records the delivery it planned.
type stringPayloads struct {
	accepts bool
	planned *Planned
}

func (d *stringPayloads) Accepts(payload *anypb.Any) bool {
	return d.accepts && payload.MessageIs(&wrapperspb.StringValue{})
}

func (d *stringPayloads) CheckIdentity([]byte) error { return nil }

func (d *stringPayloads) Plan(_ context.Context, p Planned) (Commit, error) {
	d.planned = &p
	return Commit{}, nil
}

func payload(t *testing.T, m proto.Message) *anypb.Any {
	t.Helper()
	a, err := anypb.New(m)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A payload reaches the plugin that accepts it; a plugin that accepts none is planned too, with
// no payloads, and every plugin sees the rest of the delivery.
func TestPlanHandsPayloadsToTheirPlugin(t *testing.T) {
	takes, none := &stringPayloads{accepts: true}, &stringPayloads{}
	share := payload(t, wrapperspb.String("share"))
	commits, err := Plan(context.Background(), []Delivery{takes, none}, []*anypb.Any{share}, Planned{OSCA: "ca", Trust: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Errorf("%d commits for 2 plugins", len(commits))
	}
	if takes.planned == nil || len(takes.planned.Payloads) != 1 || !proto.Equal(takes.planned.Payloads[0], share) {
		t.Errorf("the accepting plugin planned %+v", takes.planned)
	}
	if none.planned == nil || len(none.planned.Payloads) != 0 {
		t.Errorf("the other plugin planned %+v", none.planned)
	}
	for _, p := range []*stringPayloads{takes, none} {
		if p.planned != nil && (p.planned.OSCA != "ca" || !p.planned.Trust) {
			t.Errorf("a plugin planned %+v", p.planned)
		}
	}
}

// A payload no plugin accepts is refused before any plugin plans.
func TestPlanRefusesAnUnclaimedPayload(t *testing.T) {
	takes := &stringPayloads{accepts: true}
	_, err := Plan(context.Background(), []Delivery{takes}, []*anypb.Any{payload(t, wrapperspb.Int64(1))}, Planned{})
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument {
		t.Fatalf("got %v, want InvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "type.googleapis.com/google.protobuf.Int64Value") {
		t.Errorf("%v names no payload type", err)
	}
	if takes.planned != nil {
		t.Error("a plugin planned the refused delivery")
	}
}

// A payload two plugins accept goes to the first of them, as the registry lists them by name.
func TestPlanHandsAPayloadToItsFirstTaker(t *testing.T) {
	first, second := &stringPayloads{accepts: true}, &stringPayloads{accepts: true}
	if _, err := Plan(context.Background(), []Delivery{first, second}, []*anypb.Any{payload(t, wrapperspb.String("share"))}, Planned{}); err != nil {
		t.Fatal(err)
	}
	if first.planned == nil || len(first.planned.Payloads) != 1 {
		t.Errorf("the first plugin planned %+v", first.planned)
	}
	if second.planned == nil || len(second.planned.Payloads) != 0 {
		t.Errorf("the second plugin planned %+v", second.planned)
	}
}
