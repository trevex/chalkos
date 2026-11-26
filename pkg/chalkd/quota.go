package chalkd

import (
	"context"
	"sync"

	"connectrpc.com/connect/v2"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/pki"
)

// limits are how many calls of a kind run at once: network for the network's clients together,
// half of it for each client certificate, and console for the console's socket apart, so that
// neither one client nor the network's clients together keep the others or the person at the
// console out.
type limits struct {
	network, console int
	// what names the calls, as "log streams are open", and remedy says what the caller does about
	// a refusal.
	what, remedy string
}

var (
	logLimits        = limits{network: maxLogStreams, console: 2, what: "log streams are open", remedy: "close one first"}
	diagnosticLimits = limits{network: maxDiagnosticRuns, console: 1, what: "diagnostics run", remedy: "wait for one to end"}
)

// quota counts the calls of a kind that run, by where they come from.
type quota struct {
	mu               sync.Mutex
	network, console int
	byPeer           map[string]int
}

// take takes a slot for the call of ctx, or refuses it with ResourceExhausted. release frees the
// slot; it may be called more than once.
func (q *quota) take(ctx context.Context, l limits) (release func(), err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if onConsole(ctx) {
		if q.console >= l.console {
			return nil, layer.Errorf(connect.CodeResourceExhausted, "%d %s on the console already; %s", q.console, l.what, l.remedy)
		}
		q.console++
		return sync.OnceFunc(func() { q.release(&q.console, "") }), nil
	}
	if q.network >= l.network {
		return nil, layer.Errorf(connect.CodeResourceExhausted, "%d %s already; %s", q.network, l.what, l.remedy)
	}
	// A call without a certificate, as with AnyClient, has no slots of its own.
	peer := ""
	if cert := layer.Peer(ctx); cert != nil {
		peer = pki.Fingerprint(cert.Raw)
		if q.byPeer[peer] >= max(l.network/2, 1) {
			return nil, layer.Errorf(connect.CodeResourceExhausted, "%d %s for this client certificate already, as many as one client may have; %s", q.byPeer[peer], l.what, l.remedy)
		}
		if q.byPeer == nil {
			q.byPeer = map[string]int{}
		}
		q.byPeer[peer]++
	}
	q.network++
	return sync.OnceFunc(func() { q.release(&q.network, peer) }), nil
}

// release frees a slot of count, and of peer's when peer is not empty.
func (q *quota) release(count *int, peer string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	*count--
	if peer == "" {
		return
	}
	if q.byPeer[peer]--; q.byPeer[peer] == 0 {
		delete(q.byPeer, peer)
	}
}

// running is how many calls run, on the network and on the console together.
func (q *quota) running() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.network + q.console
}
