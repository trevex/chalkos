package chalkctl

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/pki"
)

// fakeNode answers chalkctl rotate as chalkd does: it trusts and serves what it was delivered
// and reports it in its status. It serves its node certificate with chalkd's NodeCertificate,
// so it refuses clients of OS CAs it no longer trusts.
type fakeNode struct {
	nodev1connect.UnimplementedNodeServiceHandler
	lab  *rotationLab
	name string
	// kind is the node's Kubernetes kind, "" without Kubernetes.
	kind string
	cert *chalkd.NodeCertificate

	mu    sync.Mutex
	share *kpki.Share
	// kubeletClient and kubeletServing are the fingerprints of the CAs that issued the kubelet's
	// certificates.
	kubeletClient, kubeletServing string
	// down makes the node refuse every call as unreachable.
	down bool
}

func (n *fakeNode) refuse() error {
	n.mu.Lock()
	down := n.down
	n.mu.Unlock()
	if down || n.lab.failing(n.name) {
		return connect.NewError(connect.CodeUnavailable, errors.New("the node is down"))
	}
	return nil
}

func (n *fakeNode) ApplyConfig(ctx context.Context, req *connect.Request[nodev1.ApplyConfigRequest]) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	m := req.Msg
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case len(m.Trust) > 0:
		n.lab.record(n.name + " trust")
		if err := n.cert.ReplaceTrust(string(m.Trust)); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	case len(m.NodeCertificate) > 0:
		n.lab.record(n.name + " node certificate")
		if err := n.cert.Replace(string(m.NodeCertificate), string(m.NodeKey), time.Now()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	case len(m.Layers) > 0:
		n.lab.record(n.name + " share")
		if n.lab.onShare != nil {
			n.lab.onShare(n.name)
		}
		data, err := kpki.SharePayload(m.Layers[0])
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		share, err := kpki.ParseShare(data)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		n.share = &share
		// Control planes issue their kubelet's client certificate from their share; workers get
		// one in it.
		issued := share.CA.Certificate
		if share.Kubelet != nil {
			n.kubeletClient = issuerFingerprint(n.lab.t, share.Kubelet.Certificate, share.CABundle())
			break
		}
		n.kubeletClient = fingerprint(n.lab.t, issued)
	}
	return connect.NewResponse(&nodev1.ApplyConfigResponse{}), nil
}

// Info names the Kubernetes layer on a node of a kind.
func (n *fakeNode) Info(ctx context.Context, _ *connect.Request[nodev1.InfoRequest]) (*connect.Response[nodev1.InfoResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	resp := &nodev1.InfoResponse{Mode: nodev1.Mode_MODE_NORMAL}
	if n.kind != "" {
		resp.Layers = []string{layers.Kubernetes}
	}
	return connect.NewResponse(resp), nil
}

func (n *fakeNode) Status(ctx context.Context, _ *connect.Request[nodev1.StatusRequest]) (*connect.Response[nodev1.StatusResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	st, _ := n.status()
	return connect.NewResponse(st), nil
}

// status is the node's status and its Kubernetes status, nil without a share.
func (n *fakeNode) status() (*nodev1.StatusResponse, *kubernetesv1.StatusResponse) {
	var k *kubernetesv1.StatusResponse
	n.mu.Lock()
	defer n.mu.Unlock()
	t := n.lab.t
	osCAs, _ := pki.Fingerprints(n.cert.OSCA())
	st := &nodev1.StatusResponse{Trust: []*nodev1.TrustStatus{{Name: "OS CA", Fingerprints: osCAs}}}
	chain := n.cert.Current().TLS.Certificate
	nodeCA, _ := x509.ParseCertificate(chain[1])
	st.Certificates = append(st.Certificates, &nodev1.CertificateStatus{Name: "node", Issuer: pki.Fingerprint(nodeCA.Raw)})
	if s := n.share; s != nil {
		cp := n.kind == kubernetes.KindControlPlane
		bundles := []struct{ name, bundle, issuing string }{{"Kubernetes CA", s.CABundle(), s.CA.Certificate}}
		if cp {
			bundles = append(bundles,
				struct{ name, bundle, issuing string }{"front-proxy CA", s.FrontProxyCABundle(), s.FrontProxyCA.Certificate},
				struct{ name, bundle, issuing string }{"etcd CA", s.EtcdCABundle(), s.EtcdCA.Certificate})
		}
		for _, b := range bundles {
			fps, _ := pki.Fingerprints(b.bundle)
			tr := &nodev1.TrustStatus{Name: b.name, Fingerprints: fps}
			if cp {
				tr.Issuing = fingerprint(t, b.issuing)
			}
			st.Trust = append(st.Trust, tr)
		}
		if cp {
			pubs, _ := s.ServiceAccountPublicKeys()
			sa := &nodev1.TrustStatus{Name: "service-account keys"}
			for _, p := range pubs {
				fp, _ := pki.PublicKeyFingerprint(p)
				sa.Fingerprints = append(sa.Fingerprints, fp)
			}
			sa.Issuing = sa.Fingerprints[0]
			enc := &nodev1.TrustStatus{Name: "encryption keys"}
			for _, k := range s.EncryptionKeys() {
				enc.Fingerprints = append(enc.Fingerprints, k.Fingerprint())
			}
			enc.Issuing = enc.Fingerprints[0]
			st.Trust = append(st.Trust, sa, enc)
			st.Certificates = append(st.Certificates, &nodev1.CertificateStatus{Name: "node CA", Fingerprint: fingerprint(t, s.NodeCA.Certificate)})
			state := "current"
			if n.lab.held("stale", n.name) {
				state = "stale"
			}
			k = &kubernetesv1.StatusResponse{Kind: n.kind, State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, ControlPlane: state, ControlPlaneCurrent: state == "current"}
			n.lab.mu.Lock()
			if s, ok := n.lab.states[n.name]; ok {
				phase := kubernetesv1.Phase_PHASE_WAITING
				if s == "no share" {
					phase = kubernetesv1.Phase_PHASE_PREPARING
				}
				k = &kubernetesv1.StatusResponse{Kind: n.kind, State: s, Phase: phase}
			} else {
				k.RevokedNodes, k.RevokedNodesError = n.lab.revoked, n.lab.revokedErr[n.name]
				k.RevokedNodesRead = k.RevokedNodesError == "" && !n.lab.unreported[n.name]
			}
			n.lab.mu.Unlock()
		} else {
			k = &kubernetesv1.StatusResponse{Kind: n.kind, State: "joined", Phase: kubernetesv1.Phase_PHASE_JOINED}
		}
		st.Certificates = append(st.Certificates,
			&nodev1.CertificateStatus{Name: "kubelet client", Issuer: n.kubeletClient},
			&nodev1.CertificateStatus{Name: "kubelet serving", Issuer: n.kubeletServing})
	}
	return st, k
}

// fakeKubernetes is a fakeNode's Kubernetes layer.
type fakeKubernetes struct {
	*fakeNode
	kubernetesv1connect.UnimplementedKubernetesServiceHandler
}

func (n fakeKubernetes) Status(ctx context.Context, _ *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	if n.kind == "" {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("the kubernetes layer is not in this image"))
	}
	if _, k := n.status(); k != nil {
		return connect.NewResponse(k), nil
	}
	return connect.NewResponse(&kubernetesv1.StatusResponse{Kind: n.kind, State: "no share", Phase: kubernetesv1.Phase_PHASE_PREPARING}), nil
}

func (n fakeKubernetes) EtcdMembers(ctx context.Context, _ *connect.Request[kubernetesv1.EtcdMembersRequest]) (*connect.Response[kubernetesv1.EtcdMembersResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	if n.kind != kubernetes.KindControlPlane {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is a worker; ask a control-plane node"))
	}
	n.lab.record(n.name + " quorum")
	members := []*kubernetesv1.EtcdMember{{Name: "cp1"}, {Name: "cp2"}}
	switch {
	case n.lab.held("learner", ""):
		members[1].Learner = true
	case n.lab.held("unhealthy", ""):
		members[1].Unhealthy = "no answer"
	}
	return connect.NewResponse(&kubernetesv1.EtcdMembersResponse{Members: members}), nil
}

func (n fakeKubernetes) RotationStep(ctx context.Context, req *connect.Request[kubernetesv1.RotationStepRequest]) (*connect.Response[kubernetesv1.RotationStepResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	n.lab.mu.Lock()
	slow := n.lab.slowRewrite
	n.lab.mu.Unlock()
	if slow && req.Msg.Step == kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lab.record(fmt.Sprintf("%s %s", n.name, strings.TrimPrefix(req.Msg.Step.String(), "ROTATION_STEP_")))
	resp := &kubernetesv1.RotationStepResponse{}
	l := n.lab
	switch req.Msg.Step {
	case kubernetesv1.RotationStep_ROTATION_STEP_RENEW_KUBELET_SERVING:
		n.kubeletServing = fingerprint(l.t, n.share.CA.Certificate)
	case kubernetesv1.RotationStep_ROTATION_STEP_RESTART_ADDONS:
		resp.Restarted = []string{"kube-system/Deployment/coredns", "apps/DaemonSet/on-delete"}
		resp.NotWaited = []string{"apps/DaemonSet/on-delete"}
	case kubernetesv1.RotationStep_ROTATION_STEP_LIST_TOKEN_SECRETS:
		resp.TokenSecrets = []string{"default/legacy"}
	case kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED:
		l.mu.Lock()
		for key, count := range l.encrypted {
			delete(l.encrypted, key)
			l.encrypted[n.share.EncryptionKeys()[0].Name] += count
			resp.Rewritten += count
		}
		l.mu.Unlock()
		fallthrough
	case kubernetesv1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED:
		l.mu.Lock()
		for key, count := range l.encrypted {
			resp.Encrypted = append(resp.Encrypted, &kubernetesv1.EncryptedObjects{Resource: "secrets", Key: key, Objects: count})
		}
		l.mu.Unlock()
	}
	return connect.NewResponse(resp), nil
}

func fingerprint(t *testing.T, certificate string) string {
	t.Helper()
	fp, err := layer.FingerprintOf(certificate)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// issuerFingerprint is the fingerprint of the CA of bundle that issued certificate.
func issuerFingerprint(t *testing.T, certificate, bundle string) string {
	t.Helper()
	cert, _ := pki.ParseCertificate([]byte(certificate))
	cas, _ := pki.ParseBundle(bundle)
	for _, ca := range cas {
		if cert.CheckSignatureFrom(ca) == nil {
			return pki.Fingerprint(ca.Raw)
		}
	}
	return ""
}

// rotationLab is a cluster of fake nodes and chalkctl with its secrets file.
type rotationLab struct {
	t     *testing.T
	ta    *testApp
	nodes map[string]*fakeNode
	addrs map[string]string

	mu sync.Mutex
	// events records what the nodes were asked, in order.
	events []string
	// onShare, when set, runs as a node receives a share.
	onShare func(node string)
	// hold makes the nodes report what holds a control plane back, a learner, an unhealthy
	// member or a stale control plane, for its polls; node limits a stale one to that node.
	hold struct {
		what, node string
		polls      int
	}
	// failIn makes the failNodes refuse every call while the secrets file records that phase
	// not yet applied.
	failIn    clustersecrets.Phase
	failNodes []string
	// encrypted counts the objects etcd holds by key name.
	encrypted map[string]uint64
	// slowRewrite makes the rewrite of encrypted objects last until the call times out.
	slowRewrite bool
	// revoked are the nodes the control planes list as revoked; revokedErr says, by control plane,
	// why one could not read them.
	revoked    []*kubernetesv1.RevokedNode
	revokedErr map[string]string
	// unreported makes control planes report no revoked nodes, as a chalkd from before
	// revocation; states makes them report a Kubernetes state other than bootstrapped.
	unreported map[string]bool
	states     map[string]string
}

func (l *rotationLab) record(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

// holdFor makes the nodes report what for the next polls.
func (l *rotationLab) holdFor(what, node string, polls int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hold.what, l.hold.node, l.hold.polls = what, node, polls
}

// held reports whether the node is to report what now, counting the poll; the last one records
// that the hold cleared.
func (l *rotationLab) held(what, node string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := &l.hold
	if h.what != what || h.polls == 0 || node != "" && h.node != node {
		return false
	}
	if h.polls--; h.polls == 0 {
		l.events = append(l.events, what+" cleared")
	}
	return true
}

// failing reports whether the node is to refuse calls now.
func (l *rotationLab) failing(node string) bool {
	l.mu.Lock()
	phase, nodes := l.failIn, l.failNodes
	l.mu.Unlock()
	if phase == "" || !slices.Contains(nodes, node) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(l.ta.dir, "secrets.json"))
	if err != nil {
		return false
	}
	s, err := clustersecrets.Read(data, nil)
	return err == nil && s.Rotation != nil && s.Rotation.Phase == phase && !s.Rotation.Applied
}

func (l *rotationLab) takeEvents() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.events
	l.events = nil
	return events
}

func newRotationLab(t *testing.T) *rotationLab {
	t.Helper()
	// Two control planes, a worker and a node without Kubernetes.
	return newRotationLabOf(t, []testNode{
		{"cp1", kubernetes.KindControlPlane, "cp"}, {"cp2", kubernetes.KindControlPlane, "cp"},
		{"w1", kubernetes.KindWorker, "w"}, {"n1", "", "plain"},
	})
}

// newRotationLabOf builds a lab of the nodes.
func newRotationLabOf(t *testing.T, nodes []testNode) *rotationLab {
	t.Helper()
	ta := newTestApp(t)
	ta.pollInterval = 5 * time.Millisecond
	ta.inventory = writeNodes(t, ta.dir, nodes, nil)
	l := &rotationLab{t: t, ta: ta, nodes: map[string]*fakeNode{}, addrs: map[string]string{}, encrypted: map[string]uint64{clustersecrets.DefaultEncryptionKeyName: 3}}
	for _, node := range nodes {
		name := node.name
		n := &fakeNode{lab: l, name: name, kind: node.kind}
		dir := filepath.Join(t.TempDir(), "chalkd")
		cert, err := pki.IssueNode(ta.secrets.NodeCA, pki.NodeNames{CommonName: name, DNSNames: []string{name}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, chalkd.NodeCertificateFile), cert.Certificate+cert.Key)
		writeFile(t, filepath.Join(dir, chalkd.CAFile), ta.secrets.OSCA.Certificate)
		if n.cert, err = chalkd.LoadNodeCertificate(dir); err != nil {
			t.Fatal(err)
		}
		if n.kind != "" {
			share, err := kpki.ShareFor(&ta.secrets, n.kind, name, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			n.share = &share
			n.kubeletClient = fingerprint(t, ta.secrets.Kubernetes.CA.Certificate)
			n.kubeletServing = n.kubeletClient
		}
		mux := http.NewServeMux()
		mux.Handle(nodev1connect.NewNodeServiceHandler(n))
		mux.Handle(kubernetesv1connect.NewKubernetesServiceHandler(fakeKubernetes{fakeNode: n}))
		l.addrs[name] = serveTLS(t, mux, chalkd.TLSConfig(n.cert.GetCertificate, n.cert.ClientCAs))
		l.nodes[name] = n
	}
	return l
}

// rotate runs chalkctl rotate with the lab's cluster.
func (l *rotationLab) rotate(args ...string) error {
	args = append([]string{"rotate"}, args...)
	args = append(args, "--inventory", l.ta.inventory)
	for name, addr := range l.addrs {
		args = append(args, "--address", name+"="+addr)
	}
	return l.ta.run(context.Background(), args)
}

// secrets reads the lab's secrets file.
func (l *rotationLab) secrets() clustersecrets.Secrets {
	l.t.Helper()
	data, err := os.ReadFile(filepath.Join(l.ta.dir, "secrets.json"))
	if err != nil {
		l.t.Fatal(err)
	}
	s, err := clustersecrets.Read(data, nil)
	if err != nil {
		l.t.Fatal(err)
	}
	return s
}

// phase is the phase the secrets file records, "" without a rotation.
func (l *rotationLab) phase() string {
	r := l.secrets().Rotation
	if r == nil {
		return ""
	}
	if !r.Applied {
		return string(r.Phase)
	}
	return string(r.Phase) + " applied"
}

// TestRotateOSCA runs the OS CA's rotation: every node trusts both OS CAs and the rotation pauses
// for new client files, control planes get the new node CA, every node a certificate of it, and
// the finish leaves the new OS CA alone, after which a client file of the old one is refused and
// one issued at the pause still works.
func TestRotateOSCA(t *testing.T) {
	l := newRotationLab(t)
	oldConfig := l.ta.writeConfig(t, "alice", pki.RoleReader, time.Hour, time.Now())
	if err := l.rotate("os-ca"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "accept applied" {
		t.Fatalf("the secrets file records %q, want the accept phase applied and the rotation paused", l.phase())
	}
	for _, want := range []string{"Issue new client files now", "chalkctl config new", "--resume"} {
		if !strings.Contains(l.ta.stdout.String(), want) {
			t.Errorf("the pause after the accept phase does not say %q: %s", want, l.ta.stdout)
		}
	}
	// A client file issued at the pause carries both roots and works throughout.
	carol := filepath.Join(l.ta.dir, "carol.json")
	if err := l.ta.run(context.Background(), []string{"config", "new", "--name", "carol", "--role", "reader", "--out", carol}); err != nil {
		t.Fatal(err)
	}
	if creds, err := l.ta.configCredentials(carol); err != nil || strings.Count(creds.osCA, "BEGIN CERTIFICATE") != 2 {
		t.Fatalf("the client file issued at the pause: %v, want both OS CAs", err)
	}
	if _, err := l.statusWith(carol, "w1"); err != nil {
		t.Errorf("a client file issued at the pause: %v", err)
	}

	l.ta.stdout.Reset()
	if err := l.rotate("os-ca", "--resume"); err != nil {
		t.Fatal(err)
	}
	s := l.secrets()
	if l.phase() != "refresh applied" {
		t.Fatalf("the secrets file records %q, want the refresh applied", l.phase())
	}
	both, _ := pki.Fingerprints(s.OSCABundle())
	nodeCA := fingerprint(t, s.NodeCA.Certificate)
	for name, n := range l.nodes {
		if fps, _ := pki.Fingerprints(n.cert.OSCA()); !slices.Equal(slices.Sorted(slices.Values(fps)), slices.Sorted(slices.Values(both))) || len(fps) != 2 {
			t.Errorf("%s trusts %d OS CAs, want both", name, len(fps))
		}
		if chain := n.cert.Current().TLS.Certificate; fingerprintDER(chain[1]) != nodeCA {
			t.Errorf("%s does not serve a certificate of the new node CA", name)
		}
		if n.kind == kubernetes.KindControlPlane && *n.share.NodeCA != s.NodeCA {
			t.Errorf("%s does not hold the new node CA", name)
		}
	}
	for _, want := range []string{"serves a certificate of the new node CA", "chalkctl config new", "--finish"} {
		if !strings.Contains(l.ta.stdout.String(), want) {
			t.Errorf("the pause after the refresh does not say %q: %s", want, l.ta.stdout)
		}
	}
	if _, err := l.statusWith(carol, "w1"); err != nil {
		t.Errorf("after the refresh, a client file issued at the pause: %v", err)
	}
	// Before the finish the old client file still authenticates; it cannot verify the nodes'
	// new certificates any more.
	if _, err := l.statusWith(oldConfig, "w1"); err == nil {
		t.Error("a client file of the old OS CA verified a node serving a certificate of the new node CA")
	}
	if err := l.authenticatesWith(oldConfig, "w1"); err != nil {
		t.Errorf("before the finish, a node refused the certificate of a client file of the old OS CA: %v", err)
	}
	if err := l.rotate("os-ca", "--finish"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "" {
		t.Fatalf("the secrets file still records %q", l.phase())
	}
	s = l.secrets()
	if len(s.Accepted.OSCA) != 0 {
		t.Error("the secrets file still accepts the old OS CA")
	}
	only, _ := pki.Fingerprints(s.OSCABundle())
	for name, n := range l.nodes {
		if fps, _ := pki.Fingerprints(n.cert.OSCA()); !slices.Equal(fps, only) {
			t.Errorf("%s still trusts %d OS CAs", name, len(fps))
		}
	}
	// A client file of the old OS CA is refused; one issued at the pause or now works.
	if err := l.ta.run(context.Background(), []string{"status", "w1", "--config", oldConfig, "--inventory", l.ta.inventory, "--address", l.addrs["w1"]}); err == nil {
		t.Error("a client file of the old OS CA reached a node after the finish")
	}
	if err := l.authenticatesWith(oldConfig, "w1"); err == nil {
		t.Error("after the finish, a node accepted the certificate of a client file of the old OS CA")
	}
	if _, err := l.statusWith(carol, "w1"); err != nil {
		t.Errorf("after the finish, a client file issued at the pause: %v", err)
	}
	l.ta.secrets = s
	newConfig := l.ta.writeConfig(t, "bob", pki.RoleReader, time.Hour, time.Now())
	if _, err := l.statusWith(newConfig, "w1"); err != nil {
		t.Errorf("a client file of the new OS CA: %v", err)
	}
	if prev, err := os.ReadFile(filepath.Join(l.ta.dir, "secrets.json.prev")); err != nil || len(prev) == 0 {
		t.Errorf("no previous version of the secrets file: %v", err)
	}
}

func fingerprintDER(der []byte) string { return pki.Fingerprint(der) }

// statusWith reads a node's status with a client file.
func (l *rotationLab) statusWith(config, node string) (*nodev1.StatusResponse, error) {
	creds, err := l.ta.configCredentials(config)
	if err != nil {
		return nil, err
	}
	pool, err := creds.roots()
	if err != nil {
		return nil, err
	}
	conn, err := dialWith(l.addrs[node], node, pool, creds)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	resp, err := conn.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// TestRotateResumesAfterAnUnreachableNode checks that a node that does not answer stops the phase,
// naming it and --resume, that the secrets file records the phase as not applied, and that
// --resume completes it once the node answers.
func TestRotateResumesAfterAnUnreachableNode(t *testing.T) {
	l := newRotationLab(t)
	l.nodes["w1"].down = true
	err := l.rotate("os-ca")
	if err == nil || !strings.Contains(err.Error(), "w1") || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("err = %v, want the phase stopped naming w1 and --resume", err)
	}
	if l.phase() != string(clustersecrets.PhaseAccept) {
		t.Fatalf("the secrets file records %q, want the accept phase not applied", l.phase())
	}
	// The other nodes took the phase.
	if fps, _ := pki.Fingerprints(l.nodes["cp1"].cert.OSCA()); len(fps) != 2 {
		t.Error("cp1 did not take the accept phase")
	}
	l.nodes["w1"].down = false
	if err := l.rotate("os-ca", "--resume"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "accept applied" {
		t.Errorf("after --resume the secrets file records %q", l.phase())
	}
	if err := l.rotate("os-ca", "--resume"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "refresh applied" {
		t.Errorf("after the second --resume the secrets file records %q", l.phase())
	}
	// Resuming once more repeats nothing that changed a node.
	l.takeEvents()
	if err := l.rotate("os-ca", "--resume"); err != nil {
		t.Fatal(err)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("resuming at the pause asked the nodes %v", events)
	}
}

// dialWith reaches a node with a client file's credentials.
func dialWith(addr, node string, roots *x509.CertPool, creds *credentials) (*client.Conn, error) {
	return client.Dial(addr, client.Options{Verify: client.ByCA(roots, node), Certificate: creds.cert})
}

// authenticatesWith reads a node's status with a client file's certificate without verifying
// the node, so it tells whether the node accepts the certificate.
func (l *rotationLab) authenticatesWith(config, node string) error {
	creds, err := l.ta.configCredentials(config)
	if err != nil {
		return err
	}
	conn, err := client.Dial(l.addrs[node], client.Options{Verify: client.AnyCertificate(), Certificate: creds.cert})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	return err
}

// TestRotateControlPlanesWaitForEachOther checks, for every kind that restarts control planes,
// that cp2 takes a phase only once cp1 runs on its new files again and etcd has every member a
// healthy voter: it gets no share while cp1 reports a learner, an unhealthy member or a stale
// control plane.
func TestRotateControlPlanesWaitForEachOther(t *testing.T) {
	for _, kind := range []clustersecrets.RotationKind{clustersecrets.RotateKubernetesCA, clustersecrets.RotateServiceAccountKey, clustersecrets.RotateEncryptionKey} {
		for _, hold := range []string{"learner", "unhealthy", "stale"} {
			t.Run(string(kind)+"/"+hold, func(t *testing.T) {
				l := newRotationLab(t)
				var once sync.Once
				l.onShare = func(node string) {
					if node == "cp1" {
						once.Do(func() { l.holdFor(hold, "cp1", 5) })
					}
				}
				if err := l.rotate(string(kind)); err != nil {
					t.Fatal(err)
				}
				events := l.takeEvents()
				cp1, cleared, cp2 := slices.Index(events, "cp1 share"), slices.Index(events, hold+" cleared"), slices.Index(events, "cp2 share")
				if cp1 < 0 || cleared < cp1 || cp2 < cleared {
					t.Errorf("events %v: want cp1's share, cp1 held back for five polls, and only then cp2's share", events)
				}
			})
		}
	}
}

// TestRotateTimeoutNamesTheNode checks that a node that does not confirm a phase within the
// timeout stops it, naming the node and what it waited for.
func TestRotateTimeoutNamesTheNode(t *testing.T) {
	l := newRotationLab(t)
	l.onShare = func(node string) {
		if node == "cp2" {
			l.holdFor("stale", "cp2", 1<<30)
		}
	}
	err := l.rotate("encryption-key", "--timeout", "200ms")
	if err == nil || !strings.Contains(err.Error(), "cp2") || !strings.Contains(err.Error(), "waiting for") || !strings.Contains(err.Error(), "stale") {
		t.Errorf("err = %v, want the phase stopped naming cp2 and its stale control plane", err)
	}
	if l.phase() != string(clustersecrets.PhaseAccept) {
		t.Errorf("the secrets file records %q, want the accept phase not applied", l.phase())
	}
}

// TestRotateResumesEveryPhase interrupts each phase of each kind at a node that stops answering,
// and checks that the phase stays unapplied and that --resume completes the rotation once the
// node answers again.
func TestRotateResumesEveryPhase(t *testing.T) {
	for _, kind := range rotationKinds() {
		for _, phase := range []clustersecrets.Phase{clustersecrets.PhaseAccept, clustersecrets.PhaseSwitch, clustersecrets.PhaseRefresh, clustersecrets.PhaseFinish} {
			// The node CA's accept and finish reach no node, so no node can stop them.
			if kind == clustersecrets.RotateNodeCA && (phase == clustersecrets.PhaseAccept || phase == clustersecrets.PhaseFinish) {
				continue
			}
			t.Run(string(kind)+"/"+string(phase), func(t *testing.T) {
				l := newRotationLab(t)
				failing := []string{"cp2"}
				// The refresh of these kinds runs through whichever control plane answers.
				if phase == clustersecrets.PhaseRefresh && (kind == clustersecrets.RotateServiceAccountKey || kind == clustersecrets.RotateEncryptionKey) {
					failing = []string{"cp1", "cp2"}
				}
				l.mu.Lock()
				l.failIn, l.failNodes = phase, failing
				l.mu.Unlock()
				// next runs what continues the rotation from where the secrets file is.
				next := func() error {
					args := []string{string(kind), "--timeout", "1s"}
					switch r := l.secrets().Rotation; {
					case r == nil:
					case r.Phase == clustersecrets.PhaseRefresh && r.Applied:
						args = append(args, "--finish", "--force")
					default:
						args = append(args, "--resume")
					}
					return l.rotate(args...)
				}
				var err error
				for range 5 {
					if err = next(); err != nil {
						break
					}
				}
				if err == nil || !strings.Contains(err.Error(), "--resume") {
					t.Fatalf("err = %v, want the %s phase stopped naming --resume", err, phase)
				}
				if l.phase() != string(phase) {
					t.Fatalf("the secrets file records %q, want the %s phase not applied", l.phase(), phase)
				}
				l.mu.Lock()
				l.failIn = ""
				l.mu.Unlock()
				for i := 0; l.phase() != "" || i == 0; i++ {
					if i == 5 {
						t.Fatalf("the rotation did not end; the secrets file records %q", l.phase())
					}
					if err := next(); err != nil {
						t.Fatal(err)
					}
				}
				s := l.secrets()
				for name, n := range l.nodes {
					if fps, _ := pki.Fingerprints(n.cert.OSCA()); kind == clustersecrets.RotateOSCA && len(fps) != 1 {
						t.Errorf("%s trusts %d OS CAs after the rotation", name, len(fps))
					}
					if n.share != nil && n.share.Accepted != nil {
						t.Errorf("%s still accepts %v", name, n.share.Accepted)
					}
				}
				if s.Rotation != nil || s.Accepted.OSCA != nil {
					t.Errorf("the secrets file still records the rotation: %v", s)
				}
			})
		}
	}
}

// TestSecretsLockedWhileChanged checks that while a rotation changes the secrets file, another
// rotation is refused at once, and that the lock is released at the end.
func TestSecretsLockedWhileChanged(t *testing.T) {
	l := newRotationLab(t)
	path := filepath.Join(l.ta.dir, "secrets.json")
	var refused []error
	l.onShare = func(string) {
		if refused != nil {
			return
		}
		// A second chalkctl with its own output, as another process would be.
		second := *l.ta.app
		second.stdout, second.stderr = &bytes.Buffer{}, &bytes.Buffer{}
		base := []string{"--inventory", l.ta.inventory, "--secrets", path}
		for _, args := range [][]string{{"rotate", "kubernetes-ca", "--resume"}, {"rotate", "os-ca", "--finish"}, {"rotate", "node-ca"}} {
			refused = append(refused, second.run(context.Background(), append(args, base...)))
		}
	}
	if err := l.rotate("kubernetes-ca"); err != nil {
		t.Fatal(err)
	}
	if len(refused) == 0 {
		t.Fatal("no share was delivered")
	}
	for _, err := range refused {
		if err == nil || !strings.Contains(err.Error(), "another chalkctl command is changing "+path) {
			t.Errorf("err = %v, want a refusal naming the command changing %s", err, path)
		}
	}
	if info, err := os.Stat(path + ".lock"); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("%s.lock: %v, want mode 0600", path, err)
	}
	l.onShare = nil
	if err := l.rotate("kubernetes-ca", "--resume"); err != nil {
		t.Errorf("the lock outlived the rotation that held it: %v", err)
	}
}

// TestSecretsChangedMeanwhileNotOverwritten checks that a secrets file changed by something else
// while a rotation runs is not replaced, and keeps what it was changed to.
func TestSecretsChangedMeanwhileNotOverwritten(t *testing.T) {
	l := newRotationLab(t)
	path := filepath.Join(l.ta.dir, "secrets.json")
	var changed []byte
	l.onShare = func(string) {
		if changed != nil {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
			return
		}
		changed = append(data, '\n')
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Error(err)
		}
	}
	err := l.rotate("kubernetes-ca")
	if err == nil || !strings.Contains(err.Error(), path+" changed since chalkctl read it") {
		t.Errorf("err = %v, want a refusal saying %s changed", err, path)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, changed) {
		t.Errorf("%s was overwritten after it changed", path)
	}
}

// TestRotateSkipsRevokedNodes leaves the nodes the first control plane that answers lists as
// revoked out of every phase, and names them.
func TestRotateSkipsRevokedNodes(t *testing.T) {
	l := newRotationLab(t)
	l.revoked = []*kubernetesv1.RevokedNode{{Name: "w1", RevokedAt: timestamppb.New(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))}, {Name: "gone"}}
	l.revokedErr = map[string]string{"cp1": "read the revoked nodes: connection refused"}
	if err := l.rotate("os-ca"); err != nil {
		t.Fatal(err)
	}
	if err := l.rotate("os-ca", "--resume"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "refresh applied" {
		t.Fatalf("the secrets file records %q, want the refresh applied", l.phase())
	}
	out := l.ta.stdout.String()
	if strings.Count(out, "skipping w1: revoked since 2026-10-23T12:00:00Z\n") != 2 || strings.Contains(out, "gone") {
		t.Errorf("stdout does not skip w1 alone in each run:\n%s", out)
	}
	for _, e := range l.takeEvents() {
		if strings.HasPrefix(e, "w1 ") {
			t.Errorf("the rotation asked w1: %s", e)
		}
	}
	if fps, _ := pki.Fingerprints(l.nodes["w1"].cert.OSCA()); len(fps) != 1 {
		t.Errorf("w1 trusts %d OS CAs, want the old one alone", len(fps))
	}
}

// TestRotateRefusesWithoutRevokedNodes starts no rotation while no control plane says which
// nodes are revoked, and needs no control plane on a cluster without Kubernetes.
func TestRotateRefusesWithoutRevokedNodes(t *testing.T) {
	l := newRotationLab(t)
	l.revokedErr = map[string]string{"cp1": "read the revoked nodes: connection refused"}
	l.nodes["cp2"].down = true
	err := l.rotate("os-ca")
	if err == nil || !strings.Contains(err.Error(), "revoked nodes") || !strings.Contains(err.Error(), "cp1: ") || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "cp2: ") {
		t.Errorf("err = %v, want a refusal naming both control planes", err)
	}
	if l.phase() != "" {
		t.Errorf("the secrets file records %q, want no rotation", l.phase())
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("the refused rotation asked the nodes %v", events)
	}

	plain := newRotationLab(t)
	plain.ta.inventory = writeNodes(t, plain.ta.dir, []testNode{{"n1", "", "plain"}}, nil)
	for name := range plain.nodes {
		if name != "n1" {
			plain.nodes[name].down = true
			delete(plain.addrs, name)
		}
	}
	if err := plain.rotate("os-ca"); err != nil {
		t.Fatal(err)
	}
	if plain.phase() != "accept applied" || strings.Contains(plain.ta.stdout.String(), "skipping") {
		t.Errorf("without Kubernetes the secrets file records %q:\n%s", plain.phase(), plain.ta.stdout)
	}
}

// TestRotateRefusesRevokedNodesNotReported takes no empty list from a control plane that does not
// say it read the list, as one whose chalkd predates revocation.
func TestRotateRefusesRevokedNodesNotReported(t *testing.T) {
	l := newRotationLab(t)
	l.unreported = map[string]bool{"cp1": true, "cp2": true}
	err := l.rotate("os-ca")
	if err == nil || !strings.Contains(err.Error(), "cp1: reports no revoked nodes") || !strings.Contains(err.Error(), "cp2: ") {
		t.Errorf("err = %v, want a refusal naming both control planes", err)
	}
	if l.phase() != "" {
		t.Errorf("the secrets file records %q, want no rotation", l.phase())
	}
}

// TestRotateBeforeBootstrap takes a cluster whose control planes all answer and none of which is
// bootstrapped to have no revoked nodes; one that does not answer leaves that unknown.
func TestRotateBeforeBootstrap(t *testing.T) {
	l := newRotationLab(t)
	l.states = map[string]string{"cp1": "no share", "cp2": "waiting for the first control plane"}
	if err := l.rotate("os-ca"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "accept applied" {
		t.Errorf("the secrets file records %q", l.phase())
	}

	down := newRotationLab(t)
	down.states = map[string]string{"cp1": "no share"}
	down.nodes["cp2"].down = true
	err := down.rotate("os-ca")
	if err == nil || !strings.Contains(err.Error(), "cp1: is not bootstrapped") || !strings.Contains(err.Error(), "cp2: ") {
		t.Errorf("err = %v, want a refusal naming both control planes", err)
	}
}

// TestRotateReadsRevokedNodesBeforeEachPhase skips a node revoked while a phase ran in the phases
// after it.
func TestRotateReadsRevokedNodesBeforeEachPhase(t *testing.T) {
	l := newRotationLab(t)
	if err := l.rotate("os-ca"); err != nil {
		t.Fatal(err)
	}
	// The switch delivers the control planes their shares; w1 is revoked meanwhile.
	l.onShare = func(string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.revoked = []*kubernetesv1.RevokedNode{{Name: "w1", RevokedAt: timestamppb.New(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))}}
	}
	l.takeEvents()
	if err := l.rotate("os-ca", "--resume"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "refresh applied" {
		t.Fatalf("the secrets file records %q, want the refresh applied", l.phase())
	}
	if out := l.ta.stdout.String(); strings.Count(out, "skipping w1: revoked since 2026-10-23T12:00:00Z\n") != 1 {
		t.Errorf("stdout does not skip w1 once:\n%s", out)
	}
	for _, e := range l.takeEvents() {
		if strings.HasPrefix(e, "w1 ") {
			t.Errorf("the rotation asked w1 after its revocation: %s", e)
		}
	}
}

// TestRotateIgnoreRevoked rotates every node without reading the revoked nodes, as while no
// control plane can say which are, and warns that revoked nodes in the inventory take part.
func TestRotateIgnoreRevoked(t *testing.T) {
	l := newRotationLab(t)
	l.revoked = []*kubernetesv1.RevokedNode{{Name: "w1"}}
	l.revokedErr = map[string]string{"cp1": "read the revoked nodes: connection refused"}
	l.unreported = map[string]bool{"cp2": true}
	if err := l.rotate("os-ca", "--ignore-revoked"); err != nil {
		t.Fatal(err)
	}
	if err := l.rotate("os-ca", "--resume", "--ignore-revoked"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "refresh applied" {
		t.Fatalf("the secrets file records %q, want the refresh applied", l.phase())
	}
	if out := l.ta.stdout.String(); strings.Contains(out, "skipping") {
		t.Errorf("stdout skips a node:\n%s", out)
	}
	if fps, _ := pki.Fingerprints(l.nodes["w1"].cert.OSCA()); len(fps) != 2 {
		t.Errorf("w1 trusts %d OS CAs, want both", len(fps))
	}
	warning := "chalkctl: warning: --ignore-revoked: rotate reads no revoked nodes, so a revoked node still in the inventory receives"
	if got := l.ta.stderr.String(); strings.Count(got, warning) != 2 || !strings.Contains(got, "remove revoked nodes from the inventory first") {
		t.Errorf("stderr = %q, want the warning once a run", got)
	}
	// Without it, the rotation stops and names the flag.
	if err := l.rotate("os-ca", "--finish"); err == nil || !strings.Contains(err.Error(), "--ignore-revoked") {
		t.Errorf("err = %v, want a refusal naming --ignore-revoked", err)
	}
}

// TestRotateRefusesAKindNoLayerRotates checks that a kind no compiled layer rotates is refused
// before anything is read or sent.
func TestRotateRefusesAKindNoLayerRotates(t *testing.T) {
	saved := plugins
	plugins = nil
	t.Cleanup(func() { plugins = saved })
	ta := newTestApp(t)
	err := ta.run(context.Background(), []string{"rotate", "kubernetes-ca"})
	if err == nil || !strings.Contains(err.Error(), "no layer of chalkctl rotates") {
		t.Fatalf("err = %v, want the kind refused", err)
	}
}

// TestRotateRefusesKubernetesWithoutAControlPlane checks that a Kubernetes kind on a cluster
// without a control-plane node is refused before the secrets file records it.
func TestRotateRefusesKubernetesWithoutAControlPlane(t *testing.T) {
	l := newRotationLabOf(t, []testNode{{"r1", "", "plain"}, {"r2", "", "plain"}})
	err := l.rotate("kubernetes-ca")
	if err == nil || !strings.Contains(err.Error(), "no control-plane node") {
		t.Fatalf("err = %v, want the rotation refused", err)
	}
	if l.phase() != "" {
		t.Errorf("the secrets file records %q", l.phase())
	}
}

// TestRotateNodeCA runs the node CA's rotation in one go: the control planes hold the new node
// CA, every node serves a certificate of it, nobody's trust changes, and the secrets file records
// no rotation afterwards.
func TestRotateNodeCA(t *testing.T) {
	l := newRotationLab(t)
	before := l.secrets()
	if err := l.rotate("node-ca"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "" {
		t.Fatalf("the secrets file records %q, want the rotation ended", l.phase())
	}
	s := l.secrets()
	if s.NodeCA == before.NodeCA || s.OSCA != before.OSCA {
		t.Fatalf("node CA changed %v, OS CA kept %v", s.NodeCA != before.NodeCA, s.OSCA == before.OSCA)
	}
	nodeCA := fingerprint(t, s.NodeCA.Certificate)
	osCA, _ := pki.Fingerprints(s.OSCABundle())
	for name, n := range l.nodes {
		if chain := n.cert.Current().TLS.Certificate; fingerprintDER(chain[1]) != nodeCA {
			t.Errorf("%s does not serve a certificate of the new node CA", name)
		}
		if fps, _ := pki.Fingerprints(n.cert.OSCA()); !slices.Equal(fps, osCA) {
			t.Errorf("%s trusts %d OS CAs, want the one", name, len(fps))
		}
		if n.kind == kubernetes.KindControlPlane && *n.share.NodeCA != s.NodeCA {
			t.Errorf("%s does not hold the new node CA", name)
		}
	}
	out := l.ta.stdout.String()
	for _, want := range []string{"nothing to deliver", "cp1 renews node certificates with the new node CA", "n1 serves a certificate of the new node CA", "the rotation of the node CA is finished", "stay valid until they expire"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--finish") {
		t.Errorf("the rotation paused:\n%s", out)
	}
}

// TestRotateNodeCAWithoutHolders rotates the node CA of a cluster no node of which holds it, as
// a router fleet: every node gets a certificate of the new node CA.
func TestRotateNodeCAWithoutHolders(t *testing.T) {
	l := newRotationLabOf(t, []testNode{{"r1", "", "plain"}, {"r2", "", "plain"}})
	if err := l.rotate("node-ca"); err != nil {
		t.Fatal(err)
	}
	nodeCA := fingerprint(t, l.secrets().NodeCA.Certificate)
	for name, n := range l.nodes {
		if chain := n.cert.Current().TLS.Certificate; fingerprintDER(chain[1]) != nodeCA {
			t.Errorf("%s does not serve a certificate of the new node CA", name)
		}
	}
	if !strings.Contains(l.ta.stdout.String(), "no node renews node certificates itself") {
		t.Errorf("output:\n%s", l.ta.stdout)
	}
}

// TestRotateHelpNamesEveryKind checks that the help lists the kinds chalkctl runs.
func TestRotateHelpNamesEveryKind(t *testing.T) {
	ta := newTestApp(t)
	cmd, _, err := newCommand(ta.app).Find([]string{"rotate"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Rotate os-ca, node-ca, kubernetes-ca, service-account-key or encryption-key"; cmd.Short != want {
		t.Errorf("Short = %q, want %q", cmd.Short, want)
	}
}

// A rotation takes the coordinators first, then every other node in name order: a node without
// Kubernetes comes between the workers by its name, not after them.
func TestRotationOrdersAllNodes(t *testing.T) {
	path := writeNodes(t, t.TempDir(), []testNode{
		{"w1", kubernetes.KindWorker, "w"}, {"a0", "", "plain"}, {"cp1", kubernetes.KindControlPlane, "cp"},
	}, nil)
	c, err := loadInventory(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	r := &rotation{cluster: c, revoked: map[string]bool{}}
	if got, want := r.allNodes(), []string{"cp1", "a0", "w1"}; !slices.Equal(got, want) {
		t.Errorf("allNodes = %v, want %v", got, want)
	}
}

// TestRotationKeepsTheVersionBeforeIt leaves the secrets file's previous version at the one the
// rotation started from, whatever the phases wrote, and names it at the finish.
func TestRotationKeepsTheVersionBeforeIt(t *testing.T) {
	l := newRotationLab(t)
	path := filepath.Join(l.ta.dir, "secrets.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.rotate("node-ca"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if prev, _ := os.ReadFile(path + ".prev"); !bytes.Equal(prev, before) {
		t.Error("secrets.json.prev is not the file from before the rotation")
	}
	if want := "the version from before the rotation is " + path + ".prev"; !strings.Contains(l.ta.stdout.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, l.ta.stdout)
	}
}
