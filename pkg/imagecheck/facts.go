package imagecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/api/check/v1/checkv1connect"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
)

// maxFactRequest bounds a fact's request: its input holds the config twice, the next and the old,
// with their defaults, each delivered within chalkd's 4 MiB of a message.
const maxFactRequest = 32 << 20

// ServeFacts serves f to a checker on l, in the background, until the returned function stops it.
// A fact that cannot be loaded answers with its error, which the checker's rule reports as it
// reports any fact it cannot load. It asks f for one fact at a time.
func ServeFacts(l net.Listener, f Facts) (stop func()) {
	mux := http.NewServeMux()
	mux.Handle(checkv1connect.NewFactsServiceHandler(&factsHandler{f: f},
		connect.WithReadMaxBytes(maxFactRequest),
		// An oversized compressed request would be decompressed in full before being refused.
		connect.WithCompression("gzip", nil, nil),
	))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Serve(l)
	}()
	return func() {
		srv.Close()
		<-done
	}
}

// factsHandler serves facts one at a time: a checker may ask on several connections at once, and
// the facts chalkd loads are not for concurrent use.
type factsHandler struct {
	mu sync.Mutex
	f  Facts
}

func (h *factsHandler) Fact(_ context.Context, req *connect.Request[checkv1.FactRequest]) (*connect.Response[checkv1.FactResponse], error) {
	input, err := decodeJSON(req.Msg.GetInput())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the fact's input: %w", err))
	}
	obj, ok := input.(map[string]any)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the fact's input is no JSON object"))
	}
	h.mu.Lock()
	v, err := h.f.Fact(req.Msg.GetName(), obj)
	h.mu.Unlock()
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&checkv1.FactResponse{Value: string(data)}), nil
}

// RemoteFacts asks the facts of a check over the socket chalkd serves them on.
type RemoteFacts struct {
	ctx    context.Context
	client checkv1connect.FactsServiceClient
}

// DialFacts is the facts chalkd serves on the Unix socket at path.
func DialFacts(ctx context.Context, path string) *RemoteFacts {
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
	return &RemoteFacts{ctx: ctx, client: checkv1connect.NewFactsServiceClient(hc, "http://facts")}
}

// For is the facts of a check of next against old, both with their defaults, which command facts
// read as rules see them. It asks chalkd for each fact once.
func (r *RemoteFacts) For(next, old config.Redacted) rules.Facts {
	return &remoteInput{r: r, input: map[string]any{"config": rules.ConfigOf(next), "oldConfig": rules.ConfigOf(old)}, loaded: map[string]loaded{}}
}

type remoteInput struct {
	r      *RemoteFacts
	input  map[string]any
	loaded map[string]loaded
}

type loaded struct {
	value any
	err   error
}

// Fact asks chalkd for the fact name, the first time a rule reads it. chalkd's error reads as the
// error of a fact loaded in place: the message alone, without connect's code.
func (ri *remoteInput) Fact(name string) (any, error) {
	if l, ok := ri.loaded[name]; ok {
		return l.value, l.err
	}
	v, err := ri.ask(name)
	ri.loaded[name] = loaded{v, err}
	return v, err
}

func (ri *remoteInput) ask(name string) (any, error) {
	input, err := json.Marshal(ri.input)
	if err != nil {
		return nil, err
	}
	resp, err := ri.r.client.Fact(ri.r.ctx, connect.NewRequest(&checkv1.FactRequest{Name: name, Input: string(input)}))
	if err != nil {
		if ce, ok := errors.AsType[*connect.Error](err); ok {
			return nil, errors.New(ce.Message())
		}
		return nil, err
	}
	return decodeJSON(resp.Msg.GetValue())
}

// decodeJSON reads one JSON value with its numbers exact, as facts and documents hold them.
func decodeJSON(s string) (any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	return v, nil
}
