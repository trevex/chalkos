package client

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/pki"
)

// silentHandler accepts every Status call and never answers it.
type silentHandler struct {
	nodev1connect.UnimplementedNodeServiceHandler
}

func (silentHandler) Status(ctx context.Context, _ *nodev1.StatusRequest) (*nodev1.StatusResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// earlyHandler answers every Status call at once as the node does when its deadline, the call's
// rounded down to whole milliseconds, ends before the caller's.
type earlyHandler struct {
	nodev1connect.UnimplementedNodeServiceHandler
}

func (earlyHandler) Status(context.Context, *nodev1.StatusRequest) (*nodev1.StatusResponse, error) {
	return nil, connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded.Error())
}

func serveNode(t *testing.T, handler nodev1connect.NodeServiceHandler) string {
	t.Helper()
	self, err := pki.SelfSigned("chalkd", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := connect.NewServer()
	nodev1connect.RegisterNodeServiceHandler(server, handler)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	srv := httptest.NewUnstartedServer(mux)
	pair, err := tls.X509KeyPair([]byte(self.Certificate), []byte(self.Key))
	if err != nil {
		t.Fatal(err)
	}
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// A unary call to a node that never answers fails at the call timeout, naming the node.
func TestCallWithoutAnswerEndsAtTheCallTimeout(t *testing.T) {
	addr := serveNode(t, silentHandler{})
	c, err := Dial(addr, Options{Verify: AnyCertificate()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.callTimeout = 100 * time.Millisecond
	_, err = c.Status(context.Background(), &nodev1.StatusRequest{})
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded || err.Error() != "deadline_exceeded: chalkd on "+addr+" did not answer within 100ms" {
		t.Errorf("err = %v, want the node named as not answering", err)
	}
}

// A call's own deadline and an Unbounded context replace the call timeout.
func TestCallTimeoutGivesWay(t *testing.T) {
	addr := serveNode(t, silentHandler{})
	c, err := Dial(addr, Options{Verify: AnyCertificate()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.callTimeout = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Status(ctx, &nodev1.StatusRequest{}); connect.CodeOf(err) != connect.CodeDeadlineExceeded || time.Since(start) < 100*time.Millisecond {
		t.Errorf("own deadline: err = %v after %v, want the context's deadline", err, time.Since(start))
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer time.AfterFunc(200*time.Millisecond, cancel).Stop()
	start = time.Now()
	if _, err := c.Status(Unbounded(ctx), &nodev1.StatusRequest{}); connect.CodeOf(err) != connect.CodeCanceled || time.Since(start) < 100*time.Millisecond {
		t.Errorf("unbounded: err = %v after %v, want the context's end", err, time.Since(start))
	}
	if minutes(CallTimeout) != "2m" {
		t.Errorf("CallTimeout reads %s", minutes(CallTimeout))
	}
}

// The node's deadline_exceeded, arriving before the call timeout ends the call's own context, still
// names the node as not answering.
func TestNodesDeadlineNamesTheNode(t *testing.T) {
	addr := serveNode(t, earlyHandler{})
	c, err := Dial(addr, Options{Verify: AnyCertificate()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.callTimeout = time.Minute
	_, err = c.Status(context.Background(), &nodev1.StatusRequest{})
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded || err.Error() != "deadline_exceeded: chalkd on "+addr+" did not answer within 1m" {
		t.Errorf("err = %v, want the node named as not answering", err)
	}
}
