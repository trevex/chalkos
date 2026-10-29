package kubernetes

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// TestStatusReportsTheControlPlanesState checks that a bootstrapped control plane says whether
// it runs on its current certificates.
func TestStatusReportsTheControlPlanesState(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	if err := knode.MarkBootstrapped(s.k.paths, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.k.hooks.CheckControlPlane = func(context.Context, k8s.Cluster, kpki.Share) error { return nil }
	if got := kubernetestest.Status(t, s).ControlPlane; got != "current" {
		t.Errorf("control plane %q", got)
	}
}

func TestServesFile(t *testing.T) {
	served, _ := pki.SelfSigned("served", time.Now())
	other, _ := pki.SelfSigned("other", time.Now())
	pair, _ := tls.X509KeyPair([]byte(served.Certificate), []byte(served.Key))
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "served.crt"), served.Certificate)
	write(t, filepath.Join(dir, "other.crt"), other.Certificate)
	if err := servesFile(context.Background(), addr, filepath.Join(dir, "served.crt")); err != nil {
		t.Error(err)
	}
	if err := servesFile(context.Background(), addr, filepath.Join(dir, "other.crt")); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Errorf("another certificate: %v", err)
	}
	if err := servesFile(context.Background(), "127.0.0.1:1", filepath.Join(dir, "served.crt")); err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Errorf("nothing listening: %v", err)
	}
}
