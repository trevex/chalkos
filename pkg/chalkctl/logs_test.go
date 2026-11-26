package chalkctl

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
)

// logsNode answers Logs with one line and keeps the requests.
type logsNode struct {
	nodev1connect.UnimplementedNodeServiceHandler
	requests chan *nodev1.LogsRequest
}

func (n logsNode) Logs(_ context.Context, req *nodev1.LogsRequest, stream nodev1connect.NodeServiceLogsServerStream) error {
	n.requests <- req
	return stream.Send(&nodev1.LogsResponse{Line: "a line"})
}

// logs -f starts with the boot's last 100 entries unless --lines says, and logs alone with the
// whole boot.
func TestLogsLines(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	n := logsNode{requests: make(chan *nodev1.LogsRequest, 1)}
	server := connect.NewServer()
	nodev1connect.RegisterNodeServiceHandler(server, n)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	ta.setWrap(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == nodev1connect.NodeServiceLogsProcedure {
				mux.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	for _, tc := range []struct {
		args   []string
		follow bool
		lines  uint32
	}{
		{[]string{"logs", "n1"}, false, 0},
		{[]string{"logs", "n1", "-f"}, true, 100},
		{[]string{"logs", "n1", "-f", "-n", "0"}, true, 0},
		{[]string{"logs", "n1", "--lines", "20"}, false, 20},
	} {
		if err := ta.run(context.Background(), ta.args(tc.args, addr)); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if req := <-n.requests; req.Follow != tc.follow || req.Lines != tc.lines {
			t.Errorf("%v sent follow %v, lines %d; want %v, %d", tc.args, req.Follow, req.Lines, tc.follow, tc.lines)
		}
	}
}
