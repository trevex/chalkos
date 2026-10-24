package chalkd

import (
	"context"
	"testing"
	"time"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// apiClientLab is a bootstrapped control plane that reaches its API server with chalkd's own
// client; no API server answers.
func apiClientLab(t *testing.T) *Kubernetes {
	t.Helper()
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	write(t, k.Paths.Bootstrapped(), "")
	k.hooks.APIClient = k.ownAPIClient
	k.timing.StatusTimeout = time.Second
	return k
}

// TestStatusReusesTheAPIClient reads a control plane's status, and with it the revoked nodes,
// with one client and one certificate for its API server, and builds another for a new share.
func TestStatusReusesTheAPIClient(t *testing.T) {
	k := apiClientLab(t)
	status := func() *apiConn {
		t.Helper()
		st, err := k.status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.RevokedNodesError == "" {
			t.Fatal("the revoked nodes were read without an API server")
		}
		return k.api
	}
	first := status()
	for range 3 {
		if got := status(); got != first {
			t.Fatal("a status built another client")
		}
	}
	if first.cred.issued != 1 {
		t.Errorf("issued %d certificates, want 1", first.cred.issued)
	}

	write(t, k.Paths.Share(), string(testShare(t, k8s.KindControlPlane)))
	if got := status(); got == first || got.cred == first.cred {
		t.Error("a new share reused the client of the one before")
	}
}

// idleCloser records that CloseIdleConnections was called.
type idleCloser struct{ closed int }

func (c *idleCloser) CloseIdleConnections() { c.closed++ }

// TestAPIClientRenewsAtHalfLife builds a new client with a new certificate once the one before is
// at its half-life. Requests in flight on the old client keep their connections: only its idle
// ones are closed.
func TestAPIClientRenewsAtHalfLife(t *testing.T) {
	k := apiClientLab(t)
	if _, _, _, err := k.apiClient(k.member); err != nil {
		t.Fatal(err)
	}
	old := k.api
	transport := &idleCloser{}
	old.transport = transport
	if _, _, _, err := k.apiClient(k.member); err != nil {
		t.Fatal(err)
	}
	if k.api != old || transport.closed != 0 {
		t.Fatal("a client before its half-life was replaced")
	}
	later := time.Now().Add(credentialValidity / 2)
	old.cred.now = func() time.Time { return later }
	if _, _, _, err := k.apiClient(k.member); err != nil {
		t.Fatal(err)
	}
	if k.api == old || k.api.cred == old.cred {
		t.Fatal("the client at its half-life was kept")
	}
	if transport.closed != 1 {
		t.Errorf("closed the old client's idle connections %d times, want 1", transport.closed)
	}
	if old.cred.issued != 1 || old.cred.closed != 0 || k.api.cred.issued != 1 {
		t.Errorf("old credential issued %d and closed its connections at %d, new issued %d; want 1, 0, 1",
			old.cred.issued, old.cred.closed, k.api.cred.issued)
	}
}

// TestAPIClientWithoutKubernetes refuses on a node whose image has no Kubernetes.
func TestAPIClientWithoutKubernetes(t *testing.T) {
	var k *Kubernetes
	if _, _, _, err := k.apiClient(k.controlPlane); err == nil {
		t.Error("reached an API server without Kubernetes")
	}
}
