package chalkd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"sync"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/pki"
)

func (s *Server) RenewNodeCertificate(ctx context.Context, req *connect.Request[nodev1.RenewNodeCertificateRequest]) (*connect.Response[nodev1.RenewNodeCertificateResponse], error) {
	peer, _ := ctx.Value(peerKey{}).(*x509.Certificate)
	if peer == nil {
		return nil, failed(connect.CodeUnauthenticated, "renewing a node certificate needs the node's current one")
	}
	nodeCA, err := s.nodeCA()
	if err != nil {
		return nil, err
	}
	chain, err := signRequest(nodeCA, pki.NamesOf(peer), req.Msg.CertificateRequest, time.Now())
	if err != nil {
		return nil, err
	}
	log.Printf("issued node %s a new node certificate", peer.Subject.CommonName)
	return connect.NewResponse(&nodev1.RenewNodeCertificateResponse{CertificateChain: []byte(chain)}), nil
}

// nodeCA returns the node CA of a control-plane node's share; only control planes hold it.
func (s *Server) nodeCA() (pki.CertKey, error) {
	notControlPlane := failed(connect.CodeFailedPrecondition, "the node is not a control plane; control-plane nodes renew node certificates")
	if s.Kubernetes == nil {
		return pki.CertKey{}, notControlPlane
	}
	c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
	if err != nil {
		return pki.CertKey{}, failed(connect.CodeInternal, "%v", err)
	}
	if c.Kind != k8s.KindControlPlane {
		return pki.CertKey{}, notControlPlane
	}
	share, err := knode.ReadShare(s.Kubernetes.Paths)
	if err != nil {
		return pki.CertKey{}, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	if share.NodeCA == nil {
		return pki.CertKey{}, failed(connect.CodeFailedPrecondition, "the node's share holds no node CA")
	}
	return *share.NodeCA, nil
}

// signRequest issues a node certificate for the key of a PKCS #10 request, for the names given:
// the request's own names are ignored, so a node renews exactly the names it was issued.
func signRequest(nodeCA pki.CertKey, names pki.NodeNames, request []byte, now time.Time) (string, error) {
	csr, err := x509.ParseCertificateRequest(request)
	if err != nil {
		return "", failed(connect.CodeInvalidArgument, "parse the certificate request: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return "", failed(connect.CodeInvalidArgument, "the certificate request is not signed with its key: %v", err)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return "", failed(connect.CodeInvalidArgument, "the certificate request's key is not ECDSA P-256")
	}
	chain, err := pki.SignNode(nodeCA, names, pub, now)
	if err != nil {
		return "", failed(connect.CodeInternal, "issue the node certificate: %v", err)
	}
	return chain, nil
}

// IssueNodeCertificate obtains a node certificate for the certificate request: from the node's own
// node CA on a control plane, from the control plane at the cluster's endpoint on a worker.
func (s *Server) IssueNodeCertificate(ctx context.Context, request []byte) (string, error) {
	c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
	if err != nil {
		return "", err
	}
	if c.Kind == k8s.KindControlPlane {
		nodeCA, err := s.nodeCA()
		if err != nil {
			return "", err
		}
		return signRequest(nodeCA, pki.NamesOf(s.Certificate.Current().Leaf), request, time.Now())
	}
	host, err := c.EndpointHost()
	if err != nil {
		return "", err
	}
	return RenewThrough(ctx, s.Certificate, net.JoinHostPort(host, client.Port), request)
}

// RenewThrough asks the chalkd at addr, which must present a node certificate, for a node
// certificate, authenticating with the current one. Any node certificate is accepted: the
// endpoint may be a VIP or a name no node certificate carries. Whatever the peer returns is
// checked like a delivered certificate before it is used.
func RenewThrough(ctx context.Context, cert *NodeCertificate, addr string, request []byte) (string, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(cert.OSCA())) {
		return "", errors.New("the node's OS CA holds no certificate")
	}
	conn, err := client.Dial(addr, client.Options{CA: roots, AnyNode: true, GetClientCertificate: cert.GetClientCertificate})
	if err != nil {
		return "", err
	}
	// Renewals are hours apart: nothing is kept open in between.
	defer conn.Close()
	resp, err := conn.RenewNodeCertificate(ctx, connect.NewRequest(&nodev1.RenewNodeCertificateRequest{CertificateRequest: request}))
	if err != nil {
		return "", fmt.Errorf("the control plane at %s: %w", addr, err)
	}
	return string(resp.Msg.CertificateChain), nil
}

// Renewal renews a certificate, or a set of them, once it is due, checking about hourly. A failed
// renewal keeps what is there and is tried again with backoff.
type Renewal struct {
	// What names what is renewed, in the log.
	What string
	// Due returns when the renewal is due and when what it renews expires.
	Due func() (renewAt, expires time.Time, err error)
	// Renew renews.
	Renew func(ctx context.Context, now time.Time) error
	// Now and After are the clock; tests replace them.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
	// Check is the time between two checks; FirstRetry is the first wait after a failure, which
	// doubles up to MaxRetry.
	Check, FirstRetry, MaxRetry time.Duration
	// Timeout bounds each attempt, so a peer that accepts the connection and never answers does
	// not stop the renewal for good.
	Timeout time.Duration

	mu      sync.Mutex
	problem string
	// force asks for a renewal now, whether it is due or not.
	force chan struct{}
}

// newRenewal checks about hourly, gives each attempt a minute and retries after a minute, then up
// to hourly.
func newRenewal(what string, due func() (time.Time, time.Time, error), renew func(context.Context, time.Time) error) *Renewal {
	return &Renewal{
		What:       what,
		Due:        due,
		Renew:      renew,
		Now:        time.Now,
		After:      time.After,
		Check:      time.Hour,
		FirstRetry: time.Minute,
		MaxRetry:   time.Hour,
		Timeout:    time.Minute,
		force:      make(chan struct{}, 1),
	}
}

// NewNodeRenewal renews the node certificate once two thirds of its lifetime have passed, with a
// certificate issue obtains for a new key.
func NewNodeRenewal(cert *NodeCertificate, issue func(context.Context, []byte) (string, error)) *Renewal {
	due := func() (time.Time, time.Time, error) {
		leaf := cert.Current().Leaf
		return pki.RenewAt(leaf), leaf.NotAfter, nil
	}
	return newRenewal("node certificate", due, func(ctx context.Context, now time.Time) error {
		return renewNodeCertificate(ctx, cert, issue, now)
	})
}

// Problem says why the last renewal failed and when what it renews expires; "" while none
// failed.
func (r *Renewal) Problem() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.problem
}

func (r *Renewal) setProblem(problem string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.problem = problem
}

// Run renews when due until ctx ends. The first check comes soon after the start, so a node that
// was off when a renewal fell due renews at once.
func (r *Renewal) Run(ctx context.Context) {
	wait := jitter(r.Check / 10)
	retry := time.Duration(0)
	for {
		forced := false
		select {
		case <-ctx.Done():
			return
		case <-r.After(wait):
		case <-r.force:
			forced = true
		}
		now := r.Now()
		renewAt, expires, err := r.Due()
		if err == nil && retry == 0 && !forced && now.Before(renewAt) {
			wait = r.Check - r.Check/10 + jitter(r.Check/5)
			continue
		}
		if err == nil {
			attempt, cancel := context.WithTimeout(ctx, r.Timeout)
			err = r.Renew(attempt, now)
			cancel()
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			retry = min(max(2*retry, r.FirstRetry), r.MaxRetry)
			problem := "renewal failing: " + err.Error()
			if !expires.IsZero() {
				problem += "; expires " + expires.UTC().Format(time.RFC3339)
			}
			r.setProblem(problem)
			// Jitter keeps the nodes that lost their control plane together from all asking again
			// at once.
			wait = retry - retry/10 + jitter(retry/5)
			log.Printf("%s: %s; trying again in %v", r.What, problem, wait.Round(time.Second))
			continue
		}
		retry = 0
		r.setProblem("")
		log.Printf("%s renewed", r.What)
		wait = r.Check - r.Check/10 + jitter(r.Check/5)
	}
}

// renewNodeCertificate obtains a node certificate for a new key, checks it is for exactly the
// current certificate's names and lasts longer, and switches to it.
func renewNodeCertificate(ctx context.Context, cert *NodeCertificate, issue func(context.Context, []byte) (string, error), now time.Time) error {
	current := cert.Current().Leaf
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: current.Subject.CommonName}}, key)
	if err != nil {
		return err
	}
	chain, err := issue(ctx, request)
	if err != nil {
		return err
	}
	leaf, err := pki.ParseCertificate([]byte(chain))
	if err != nil {
		return fmt.Errorf("the issued certificate: %w", err)
	}
	if !pki.NamesOf(leaf).Equal(pki.NamesOf(current)) {
		return errors.New("the issued certificate is for other names than the node's")
	}
	if !leaf.NotAfter.After(current.NotAfter) {
		return fmt.Errorf("the issued certificate expires %s, no later than the current one", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		return err
	}
	// Replace checks that the key is the new one and that the chain leads to the OS CA.
	return cert.Replace(chain, keyPEM, now)
}

// jitter returns a random duration below d, so nodes that started together spread their checks.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}

// Force renews at once, whether the renewal is due or not. Only test images use it, through
// Server.RenewOnApplyIdentity.
func (r *Renewal) Force() {
	select {
	case r.force <- struct{}{}:
	default:
		// A forced renewal is pending already.
	}
}
