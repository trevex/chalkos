package main

import (
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

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/client"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// rotationManifest is a cluster of two control planes, a worker and a node without Kubernetes.
const rotationManifest = `{
  "schemaVersion": 0,
  "cluster": {"name": "lab", "endpoint": "https://10.0.0.10:6443"},
  "roles": {"cp": {"image": "roles.cp.image", "kind": "controlplane"}, "w": {"image": "roles.w.image", "kind": "worker"}, "plain": {"image": "roles.plain.image"}},
  "nodes": {
    "cp1": {"role": "cp", "identity": {"hostname": "cp1", "network": {"networks": {"10-uplink": {"address": ["10.0.0.11/24"]}}}, "networkUnits": {}, "labels": {}, "taints": [], "storage": {"disks": {}, "volumes": {}, "fallback": "none", "encryption": "none"}, "extensions": {}}},
    "cp2": {"role": "cp", "identity": {"hostname": "cp2", "network": {"networks": {"10-uplink": {"address": ["10.0.0.12/24"]}}}, "networkUnits": {}, "labels": {}, "taints": [], "storage": {"disks": {}, "volumes": {}, "fallback": "none", "encryption": "none"}, "extensions": {}}},
    "w1": {"role": "w", "identity": {"hostname": "w1", "network": {"networks": {"10-uplink": {"address": ["10.0.0.21/24"]}}}, "networkUnits": {}, "labels": {}, "taints": [], "storage": {"disks": {}, "volumes": {}, "fallback": "none", "encryption": "none"}, "extensions": {}}},
    "n1": {"role": "plain", "identity": {"hostname": "n1", "network": {"networks": {"10-uplink": {"address": ["10.0.0.31/24"]}}}, "networkUnits": {}, "labels": {}, "taints": [], "storage": {"disks": {}, "volumes": {}, "fallback": "none", "encryption": "none"}, "extensions": {}}}
  }
}`

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
	defer n.mu.Unlock()
	if n.down {
		return connect.NewError(connect.CodeUnavailable, errors.New("the node is down"))
	}
	return nil
}

func (n *fakeNode) ApplyIdentity(ctx context.Context, req *connect.Request[nodev1.ApplyIdentityRequest]) (*connect.Response[nodev1.ApplyIdentityResponse], error) {
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
	case len(m.KubernetesShare) > 0:
		n.lab.record(n.name + " share")
		share, err := kpki.ParseShare(m.KubernetesShare)
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
	return connect.NewResponse(&nodev1.ApplyIdentityResponse{}), nil
}

func (n *fakeNode) Status(ctx context.Context, _ *connect.Request[nodev1.StatusRequest]) (*connect.Response[nodev1.StatusResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	t := n.lab.t
	osCAs, _ := pki.Fingerprints(n.cert.OSCA())
	st := &nodev1.StatusResponse{Trust: []*nodev1.TrustStatus{{Name: "OS CA", Fingerprints: osCAs}}}
	chain := n.cert.Current().TLS.Certificate
	nodeCA, _ := x509.ParseCertificate(chain[1])
	st.Certificates = append(st.Certificates, &nodev1.CertificateStatus{Name: "node", Issuer: pki.Fingerprint(nodeCA.Raw)})
	if s := n.share; s != nil {
		cp := n.kind == manifest.KindControlPlane
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
			st.Kubernetes = &nodev1.KubernetesStatus{Kind: n.kind, State: "bootstrapped", ControlPlane: "current"}
		} else {
			st.Kubernetes = &nodev1.KubernetesStatus{Kind: n.kind, State: "joined"}
		}
		st.Certificates = append(st.Certificates,
			&nodev1.CertificateStatus{Name: "kubelet client", Issuer: n.kubeletClient},
			&nodev1.CertificateStatus{Name: "kubelet serving", Issuer: n.kubeletServing})
	}
	return connect.NewResponse(st), nil
}

func (n *fakeNode) EtcdMembers(ctx context.Context, _ *connect.Request[nodev1.EtcdMembersRequest]) (*connect.Response[nodev1.EtcdMembersResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	if n.kind != manifest.KindControlPlane {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is a worker; ask a control-plane node"))
	}
	n.lab.record(n.name + " quorum")
	return connect.NewResponse(&nodev1.EtcdMembersResponse{Members: []*nodev1.EtcdMember{{Name: "cp1"}, {Name: "cp2"}}}), nil
}

func (n *fakeNode) RotationStep(ctx context.Context, req *connect.Request[nodev1.RotationStepRequest]) (*connect.Response[nodev1.RotationStepResponse], error) {
	if err := n.refuse(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lab.record(fmt.Sprintf("%s %s", n.name, strings.TrimPrefix(req.Msg.Step.String(), "ROTATION_STEP_")))
	resp := &nodev1.RotationStepResponse{}
	l := n.lab
	switch req.Msg.Step {
	case nodev1.RotationStep_ROTATION_STEP_RENEW_KUBELET_SERVING:
		n.kubeletServing = fingerprint(l.t, n.share.CA.Certificate)
	case nodev1.RotationStep_ROTATION_STEP_RESTART_ADDONS:
		resp.Restarted = []string{"kube-system/Deployment/coredns"}
	case nodev1.RotationStep_ROTATION_STEP_LIST_TOKEN_SECRETS:
		resp.TokenSecrets = []string{"default/legacy"}
	case nodev1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED:
		l.mu.Lock()
		for key, count := range l.encrypted {
			delete(l.encrypted, key)
			l.encrypted[n.share.EncryptionKeys()[0].Name] += count
			resp.Rewritten += count
		}
		l.mu.Unlock()
		fallthrough
	case nodev1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED:
		l.mu.Lock()
		for key, count := range l.encrypted {
			resp.Encrypted = append(resp.Encrypted, &nodev1.EncryptedObjects{Resource: "secrets", Key: key, Objects: count})
		}
		l.mu.Unlock()
	}
	return connect.NewResponse(resp), nil
}

func fingerprint(t *testing.T, certificate string) string {
	t.Helper()
	fp, err := fingerprintOf(certificate)
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
	// encrypted counts the objects etcd holds by key name.
	encrypted map[string]uint64
}

func (l *rotationLab) record(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
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
	ta := newTestApp(t)
	ta.pollInterval = 5 * time.Millisecond
	writeFile(t, filepath.Join(ta.dir, "manifest.json"), rotationManifest)
	m, err := manifest.Decode(strings.NewReader(rotationManifest))
	if err != nil {
		t.Fatal(err)
	}
	l := &rotationLab{t: t, ta: ta, nodes: map[string]*fakeNode{}, addrs: map[string]string{}, encrypted: map[string]uint64{pki.DefaultEncryptionKeyName: 3}}
	for name, node := range m.Nodes {
		n := &fakeNode{lab: l, name: name, kind: m.Roles[node.Role].Kind}
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
		_, h := nodev1connect.NewNodeServiceHandler(n)
		l.addrs[name] = serveTLS(t, http.Handler(h), chalkd.TLSConfig(n.cert.GetCertificate, n.cert.ClientCAs))
		l.nodes[name] = n
	}
	return l
}

// rotate runs chalkctl rotate with the lab's cluster.
func (l *rotationLab) rotate(args ...string) error {
	args = append([]string{"rotate"}, args...)
	args = append(args, "--manifest", filepath.Join(l.ta.dir, "manifest.json"), "--flake", l.ta.dir)
	for name, addr := range l.addrs {
		args = append(args, "--endpoint", name+"="+addr)
	}
	return l.ta.run(context.Background(), args)
}

// secrets reads the lab's secrets file.
func (l *rotationLab) secrets() pki.Secrets {
	l.t.Helper()
	data, err := os.ReadFile(filepath.Join(l.ta.dir, "secrets.json"))
	if err != nil {
		l.t.Fatal(err)
	}
	s, err := pki.ReadSecrets(data, nil)
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
		return r.Phase
	}
	return r.Phase + " applied"
}

// TestRotateOSCA runs the OS CA's rotation: every node trusts both OS CAs, control planes get the
// new node CA, every node a certificate of it, and the finish leaves the new OS CA alone, after
// which a client file of the old one is refused.
func TestRotateOSCA(t *testing.T) {
	l := newRotationLab(t)
	oldConfig := l.ta.writeConfig(t, "alice", pki.RoleReader, time.Hour, time.Now())
	if err := l.rotate("os-ca"); err != nil {
		t.Fatal(err)
	}
	s := l.secrets()
	if l.phase() != "refresh applied" {
		t.Fatalf("the secrets file records %q, want the refresh applied", l.phase())
	}
	both, _ := pki.Fingerprints(s.OSCABundle())
	nodeCA := fingerprint(t, s.NodeCA.Certificate)
	for name, n := range l.nodes {
		if fps, _ := pki.Fingerprints(n.cert.OSCA()); !sameFingerprints(fps, both) || len(fps) != 2 {
			t.Errorf("%s trusts %d OS CAs, want both", name, len(fps))
		}
		if chain := n.cert.Current().TLS.Certificate; fingerprintDER(chain[1]) != nodeCA {
			t.Errorf("%s does not serve a certificate of the new node CA", name)
		}
		if n.kind == manifest.KindControlPlane && *n.share.NodeCA != s.NodeCA {
			t.Errorf("%s does not hold the new node CA", name)
		}
	}
	if !strings.Contains(l.ta.stdout.String(), "chalkctl config new") {
		t.Errorf("the pause does not name chalkctl config new: %s", l.ta.stdout)
	}
	// Before the finish the old client file still authenticates; it cannot verify the nodes'
	// new certificates any more.
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
	// A client file of the old OS CA is refused; one issued now works.
	if err := l.ta.run(context.Background(), []string{"status", "w1", "--config", oldConfig, "--manifest", filepath.Join(l.ta.dir, "manifest.json"), "--flake", l.ta.dir, "--endpoint", l.addrs["w1"]}); err == nil {
		t.Error("a client file of the old OS CA reached a node after the finish")
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
	if l.phase() != pki.PhaseAccept {
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
	if l.phase() != "refresh applied" {
		t.Errorf("after --resume the secrets file records %q", l.phase())
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
	return client.Dial(addr, client.Options{CA: roots, ServerName: node, Certificate: creds.cert})
}
