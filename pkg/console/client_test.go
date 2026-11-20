package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
)

// stub serves the diagnostics and the journal that TestNodeClient reads.
type stub struct {
	nodev1connect.UnimplementedNodeServiceHandler
}

func (stub) RunDiagnostic(_ context.Context, req *nodev1.RunDiagnosticRequest, stream nodev1connect.NodeServiceRunDiagnosticServerStream) error {
	var msgs []*nodev1.RunDiagnosticResponse
	switch req.Name {
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

func (stub) Logs(_ context.Context, _ *nodev1.LogsRequest, stream nodev1connect.NodeServiceLogsServerStream) error {
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
	server := connect.NewServer()
	nodev1connect.RegisterNodeServiceHandler(server, h)
	connecthttp.Mount(m, server)
	return m
}

// The node client collects a diagnostic's standard output, fails one that exits non-zero with its
// standard error, and passes the journal's lines on.
func TestNodeClient(t *testing.T) {
	srv := httptest.NewServer(mux(stub{}))
	defer srv.Close()
	c := nodeClient{node: nodev1connect.NewNodeServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL)))}
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
