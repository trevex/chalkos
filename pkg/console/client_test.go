package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
)

// stub serves the diagnostics and the journal that TestNodeClient reads.
type stub struct {
	nodev1connect.UnimplementedNodeServiceHandler
}

func (stub) RunDiagnostic(_ context.Context, req *connect.Request[nodev1.RunDiagnosticRequest], stream *connect.ServerStream[nodev1.RunDiagnosticResponse]) error {
	var msgs []*nodev1.RunDiagnosticResponse
	switch req.Msg.Name {
	case "ok":
		msgs = []*nodev1.RunDiagnosticResponse{
			{Output: &nodev1.RunDiagnosticResponse_Stdout{Stdout: []byte("a")}},
			{Output: &nodev1.RunDiagnosticResponse_Stdout{Stdout: []byte("b")}},
			{Output: &nodev1.RunDiagnosticResponse_Exit{Exit: &nodev1.DiagnosticExit{}}},
		}
	case "bad":
		msgs = []*nodev1.RunDiagnosticResponse{
			{Output: &nodev1.RunDiagnosticResponse_Stderr{Stderr: []byte("no such table\n")}},
			{Output: &nodev1.RunDiagnosticResponse_Exit{Exit: &nodev1.DiagnosticExit{Code: 2}}},
		}
	}
	for _, m := range msgs {
		if err := stream.Send(m); err != nil {
			return err
		}
	}
	return nil
}

func (stub) Logs(_ context.Context, _ *connect.Request[nodev1.LogsRequest], stream *connect.ServerStream[nodev1.LogsResponse]) error {
	for _, l := range []string{"l1", "l2"} {
		if err := stream.Send(&nodev1.LogsResponse{Line: l}); err != nil {
			return err
		}
	}
	return nil
}

// mux serves h's node API.
func mux(h nodev1connect.NodeServiceHandler) *http.ServeMux {
	m := http.NewServeMux()
	m.Handle(nodev1connect.NewNodeServiceHandler(h))
	return m
}

// The node client collects a diagnostic's standard output, fails one that exits non-zero with its
// standard error, and passes the journal's lines on.
func TestNodeClient(t *testing.T) {
	srv := httptest.NewServer(mux(stub{}))
	defer srv.Close()
	c := nodeClient{node: nodev1connect.NewNodeServiceClient(srv.Client(), srv.URL)}
	ctx := context.Background()
	if out, err := c.Diagnostic(ctx, "ok", nil); err != nil || string(out) != "ab" {
		t.Errorf("ok: %q, %v", out, err)
	}
	if _, err := c.Diagnostic(ctx, "bad", nil); err == nil || !strings.Contains(err.Error(), "exit 2: no such table") {
		t.Errorf("bad: %v", err)
	}
	var lines []string
	if err := c.Logs(ctx, "", func(l string) { lines = append(lines, l) }); err != nil || !slices.Equal(lines, []string{"l1", "l2"}) {
		t.Errorf("logs %q, %v", lines, err)
	}
}
