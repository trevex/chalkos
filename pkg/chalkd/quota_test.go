package chalkd

import (
	"context"
	"crypto/x509"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
)

// peerContext is the context of a call of the client certificate raw.
func peerContext(raw string) context.Context {
	return layer.WithPeer(context.Background(), &x509.Certificate{Raw: []byte(raw)})
}

// fill takes n slots of q for ctx and fails the test on a refusal.
func fill(t *testing.T, q *quota, ctx context.Context, l limits, n int) []func() {
	t.Helper()
	var releases []func()
	for range n {
		release, err := q.take(ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	return releases
}

// One client certificate opens half the network's log streams, another the rest; the console
// opens its own once the network's are taken, and a released slot opens again.
func TestQuota(t *testing.T) {
	var q quota
	a, b := peerContext("a"), peerContext("b")
	console := context.WithValue(context.Background(), consoleKey{}, true)
	released := fill(t, &q, a, logLimits, maxLogStreams/2)
	if _, err := q.take(a, logLimits); layer.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "4 log streams are open for this client certificate already") {
		t.Errorf("a's fifth stream: err = %v", err)
	}
	fill(t, &q, b, logLimits, maxLogStreams/2)
	if _, err := q.take(peerContext("c"), logLimits); layer.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "8 log streams are open already; close one first") {
		t.Errorf("c's stream: err = %v", err)
	}
	fill(t, &q, console, logLimits, logLimits.console)
	if _, err := q.take(console, logLimits); layer.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "2 log streams are open on the console already") {
		t.Errorf("the console's third stream: err = %v", err)
	}
	// A release counts once, however often it is called.
	released[0]()
	released[0]()
	if n := q.running(); n != maxLogStreams-1+logLimits.console {
		t.Errorf("%d streams run", n)
	}
	fill(t, &q, a, logLimits, 1)
	if _, err := q.take(a, logLimits); layer.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("a's fifth stream after a release: err = %v", err)
	}
	// A call without a certificate counts against the network's slots alone.
	var anon quota
	fill(t, &anon, context.Background(), diagnosticLimits, maxDiagnosticRuns)
	if _, err := anon.take(context.Background(), diagnosticLimits); layer.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "4 diagnostics run already; wait for one to end") {
		t.Errorf("a fifth diagnostic: err = %v", err)
	}
}
