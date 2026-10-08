package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/trevex/chalkos/pkg/pki"
)

// chalkctlWith runs chalkctl for the node with a client file instead of the secrets file.
func chalkctlWith(t *testing.T, n *node, manifest, config string, args ...string) (string, error) {
	t.Helper()
	args = append(args, "--manifest", filepath.Join(os.Getenv("CHALKLAB_MANIFESTS"), manifest+".json"), "--config", config, "--endpoint", n.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Getenv("CHALKLAB_CHALKCTL"), args...)
	// No secrets file is at hand.
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	t.Logf("chalkctl %s:\n%s", strings.Join(args, " "), out)
	return string(out), err
}

// synchronised waits until chrony on the node follows the test's NTP server.
func synchronised(t *testing.T, n *node, name string) {
	t.Helper()
	// A status that hangs ends with the wait rather than after it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	waitFor(t, 5*time.Minute, name+"'s clock to follow the test's NTP server", func() error {
		out, err := chalkctlContext(ctx, t, n, "base", "status", name)
		if err != nil {
			return err
		}
		if !strings.Contains(out, "time: synchronised to 10.0.2.2") {
			return fmt.Errorf("status of %s:\n%s", name, out)
		}
		return nil
	})
}

// readerClientFile issues a reader's client file and checks that it reads the node's status but
// cannot reboot it.
func readerClientFile(t *testing.T, w1 *node, path string) {
	t.Helper()
	if _, err := chalkctl(t, nil, "base", "config", "new", "--name", "e2e-reader", "--role", "reader", "--out", path); err != nil {
		t.Fatal(err)
	}
	if out, err := chalkctlWith(t, w1, "base", path, "status", "w1"); err != nil || !strings.Contains(out, "kubernetes worker: joined") {
		t.Errorf("status of w1 as a reader: %v", err)
	}
	if out, err := chalkctlWith(t, w1, "base", path, "reboot", "w1"); err == nil || !strings.Contains(out, "needs the operator role") {
		t.Errorf("a reader rebooted w1: %v", err)
	}
}

// peerCertificate returns the certificate the TLS server at addr presents.
func peerCertificate(addr string) (*x509.Certificate, error) {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0], nil
}

// renewed waits until the server at addr presents another certificate than before, for the same
// names.
func renewed(t *testing.T, what, addr string, before *x509.Certificate, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, what, func() error {
		cert, err := peerCertificate(addr)
		if err != nil {
			return err
		}
		if cert.SerialNumber.Cmp(before.SerialNumber) == 0 {
			return errors.New("the certificate from before")
		}
		if !pki.NamesOf(cert).Equal(pki.NamesOf(before)) {
			return fmt.Errorf("renewed for %+v, not %+v", pki.NamesOf(cert), pki.NamesOf(before))
		}
		return nil
	})
}

// renewals renews w1's node certificate through cp1 and cp1's own certificates in place: the
// test image renews them whenever an identity is delivered.
func renewals(t *testing.T, ctx context.Context, cs kubernetes.Interface, nodes map[string]*node, apiPort int) {
	t.Helper()
	w1, cp1 := nodes["w1"], nodes["cp1"]
	w1Before, err := peerCertificate(w1.addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chalkctl(t, w1, "base", "apply-identity", "w1"); err != nil {
		t.Fatal(err)
	}
	renewed(t, "w1's node certificate renewed through cp1", w1.addr, w1Before, 2*time.Minute)
	// chalkctl verifies the new certificate by the OS CA, through the node CA.
	if out, err := chalkctl(t, w1, "base", "status", "w1"); err != nil || strings.Contains(out, "renewal failing") {
		t.Errorf("status of w1 after the renewal: %v\n%s", err, out)
	}

	cp1Before, err := peerCertificate(cp1.addr)
	if err != nil {
		t.Fatal(err)
	}
	apiBefore, err := peerCertificate(fmt.Sprintf("127.0.0.1:%d", apiPort))
	if err != nil {
		t.Fatal(err)
	}
	leaders, err := leaseHolders(ctx, cs, "kube-controller-manager", "kube-scheduler")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := chalkctl(t, cp1, "base", "apply-identity", "cp1"); err != nil {
		t.Fatal(err)
	}
	renewed(t, "cp1's node certificate renewed", cp1.addr, cp1Before, 2*time.Minute)
	renewed(t, "the API server serving renewed certificates", fmt.Sprintf("127.0.0.1:%d", apiPort), apiBefore, 5*time.Minute)
	waitFor(t, 5*time.Minute, "the API server after the renewal", func() error {
		_, err := cs.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx)
		return err
	})
	t.Logf("the control plane answered with renewed certificates after %v", time.Since(start).Round(time.Second))
	waitFor(t, 5*time.Minute, "both nodes Ready after the renewal", func() error { return nodesReady(ctx, cs, "cp1", "w1") })
	// The controller-manager and the scheduler restarted with the renewed kubeconfigs and took
	// the lead again: a restarted process holds the lease under a new identity. The clocks agree,
	// as chrony follows the test's NTP server.
	waitFor(t, 10*time.Minute, "the controller-manager and the scheduler to lead again after the renewal", func() error {
		return leadingAgain(ctx, cs, start, leaders)
	})
	t.Logf("the controller-manager and the scheduler led again after %v", time.Since(start).Round(time.Second))
	if out, err := chalkctl(t, cp1, "base", "status", "cp1"); err != nil || strings.Contains(out, "renewal failing") {
		t.Errorf("status of cp1 after the renewal: %v\n%s", err, out)
	}
}

// leaseHolders returns who holds each component's leader election lease.
func leaseHolders(ctx context.Context, cs kubernetes.Interface, components ...string) (map[string]string, error) {
	holders := map[string]string{}
	for _, name := range components {
		lease, err := cs.CoordinationV1().Leases("kube-system").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		holders[name] = valueOf(lease.Spec.HolderIdentity)
	}
	return holders, nil
}

// leadingAgain checks that each component's leader election lease was taken again since the time
// given, by another holder than before or acquired since then, and that it is still renewed. It
// says which of the control plane's pods restart when one was not.
func leadingAgain(ctx context.Context, cs kubernetes.Interface, since time.Time, before map[string]string) error {
	for name, holder := range before {
		lease, err := cs.CoordinationV1().Leases("kube-system").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		spec := lease.Spec
		takenAgain := spec.HolderIdentity != nil && *spec.HolderIdentity != "" && *spec.HolderIdentity != holder ||
			spec.AcquireTime != nil && !spec.AcquireTime.Time.Before(since)
		if !takenAgain || spec.RenewTime == nil || spec.RenewTime.Time.Before(since) {
			pods, err := cs.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
			if err != nil {
				return err
			}
			var restarts []string
			for _, p := range pods.Items {
				for _, c := range p.Status.ContainerStatuses {
					restarts = append(restarts, fmt.Sprintf("%s: ready %v, %d restarts", p.Name, c.Ready, c.RestartCount))
				}
			}
			return fmt.Errorf("%s's lease is held by %v, acquired at %v and renewed at %v; %s", name, valueOf(spec.HolderIdentity), spec.AcquireTime, spec.RenewTime, strings.Join(restarts, "; "))
		}
	}
	return nil
}

// valueOf is the string s points to, "" for none.
func valueOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
