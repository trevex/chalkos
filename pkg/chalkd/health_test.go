package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	corev1 "k8s.io/api/core/v1"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// chalkdServing stands for chalkd: a TLS server that completes handshakes.
func chalkdServing(t *testing.T) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// kubelet stands for the kubelet's health endpoint, healthy or not.
func kubelet(t *testing.T, healthy *bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !*healthy {
			http.Error(w, "not healthy", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/healthz"
}

func TestHealthWithoutKubernetes(t *testing.T) {
	addr := chalkdServing(t)
	for _, tc := range []struct {
		name   string
		chalkd string
		rules  []rule
		want   string
	}{
		{"healthy", addr, nil, ""},
		{"chalkd not serving", "127.0.0.1:1", nil, "chalkd does not serve"},
		{"identity not applied", addr, []rule{{prefix: "systemctl is-active --quiet chalkos-identity.service", err: errors.New("inactive")}}, "identity was not applied"},
		{"a failed unit", addr, []rule{{prefix: "systemctl list-units", out: "broken.service loaded failed failed Broken\n"}}, "units failed: broken.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Health{Run: &fakeRunner{rules: tc.rules}, Chalkd: tc.chalkd}
			checkHealth(t, h, tc.want)
		})
	}
}

func checkHealth(t *testing.T, h *Health, want string) {
	t.Helper()
	err := h.Check(context.Background())
	switch {
	case want == "" && err != nil:
		t.Errorf("not healthy: %v", err)
	case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
		t.Errorf("health = %v, want %q", err, want)
	}
}

func TestHealthOfAWorker(t *testing.T) {
	addr := chalkdServing(t)
	for _, tc := range []struct {
		name    string
		share   bool
		kubelet bool
		node    error
		want    string
	}{
		{"healthy", true, true, nil, ""},
		{"kubelet not healthy", true, false, nil, "the kubelet is not healthy"},
		{"Node not registered", true, true, errors.New("nodes \"n1\" not found"), "the Node is not registered"},
		// A worker not given a share yet belongs to no cluster.
		{"no share", false, false, errors.New("no kubeconfig"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := kubernetesServer(t, k8s.KindWorker, tc.share)
			s.Kubernetes.Node = func(context.Context) (*corev1.Node, error) { return readyNode(), tc.node }
			healthy := tc.kubelet
			h := &Health{Run: r, Kubernetes: s.Kubernetes, Chalkd: addr, Kubelet: kubelet(t, &healthy)}
			checkHealth(t, h, tc.want)
		})
	}
	s, r := kubernetesServer(t, k8s.KindWorker, true)
	withoutAddress(t, s, "no ipv4 node address matches validSubnets 10.0.0.0/8")
	healthy := true
	checkHealth(t, &Health{Run: r, Kubernetes: s.Kubernetes, Chalkd: addr, Kubelet: kubelet(t, &healthy)}, "preparation failed: no ipv4 node address")
}

func TestHealthOfAControlPlane(t *testing.T) {
	addr := chalkdServing(t)
	s, _, members := memberServer(t)
	k := s.Kubernetes
	ready, healthy := true, true
	k.APIServerReady = func(context.Context, k8s.Cluster, kpki.Share) bool { return ready }
	h := &Health{Run: &fakeRunner{}, Kubernetes: k, Chalkd: addr, Kubelet: kubelet(t, &healthy)}
	checkHealth(t, h, "")
	ready = false
	checkHealth(t, h, "the API server is not ready")
	ready, healthy = true, false
	checkHealth(t, h, "the kubelet is not healthy")
	healthy = true
	k.EtcdTimeout = 2 * time.Second
	members["n1"].Stop()
	checkHealth(t, h, "the etcd member is not healthy")

	// A control plane waiting for its bootstrap is healthy once its identity was applied.
	s2, r := kubernetesServer(t, k8s.KindControlPlane, true)
	if bootstrapped, _ := knode.Bootstrapped(s2.Kubernetes.Paths); bootstrapped {
		t.Fatal("bootstrapped")
	}
	checkHealth(t, &Health{Run: r, Kubernetes: s2.Kubernetes, Chalkd: addr, Kubelet: "http://127.0.0.1:1/healthz"}, "")
}

// espWith writes UKIs to an ESP, each booting the store whose root hash is its version's, and
// marks the named one as the entry the node booted.
func espWith(t *testing.T, booted string, files ...string) *Health {
	t.Helper()
	root := t.TempDir()
	h := &Health{ESP: filepath.Join(root, "esp"), EFIVars: filepath.Join(root, "efivars"), Cmdline: filepath.Join(root, "cmdline"),
		Version: "0.2.0", Record: filepath.Join(root, "var", "failed-boot")}
	hash := func(version string) string { return strings.Repeat(fmt.Sprintf("%02x", version[2]), 32) }
	for _, f := range files {
		version := strings.TrimPrefix(f, "chalkos_")[:5]
		write(t, filepath.Join(h.ESP, "EFI", "Linux", f), string(ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos", "IMAGE_VERSION": version}, "usrhash="+hash(version))))
	}
	value := []byte{7, 0, 0, 0}
	for _, u := range utf16.Encode([]rune(booted + "\x00")) {
		value = binary.LittleEndian.AppendUint16(value, u)
	}
	write(t, filepath.Join(h.EFIVars, "LoaderEntrySelected-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"), string(value))
	write(t, h.Cmdline, "init=/x usrhash="+hash(strings.TrimPrefix(booted, "chalkos_")[:5])+"\n")
	return h
}

// TestHealthWaitReboots waits for a boot that never becomes healthy, records its journal on VAR, and
// reboots only while the boot loader counts it and has somewhere to go.
func TestHealthWaitReboots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		h      *Health
		reboot bool
	}{
		{"a try with tries left", espWith(t, "chalkos_0.2.0.efi", "chalkos_0.1.0.efi", "chalkos_0.2.0+2-1.efi"), true},
		{"the last try", espWith(t, "chalkos_0.2.0.efi", "chalkos_0.1.0.efi", "chalkos_0.2.0+0-3.efi"), true},
		{"a blessed boot", espWith(t, "chalkos_0.2.0.efi", "chalkos_0.1.0+0-3.efi", "chalkos_0.2.0.efi"), false},
		{"nothing to fall back to", espWith(t, "chalkos_0.2.0.efi", "chalkos_0.2.0+0-4.efi"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{rules: []rule{{prefix: "journalctl --boot --no-pager --output=short-iso --lines=30 --unit=chalkd.service --unit=chalkos-health.service", out: "chalkd[1]: not healthy yet: chalkd does not serve\n"}}}
			tc.h.Run, tc.h.Chalkd, tc.h.Interval = r, "127.0.0.1:1", 10*time.Millisecond
			err := tc.h.Wait(context.Background(), 50*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), "did not become healthy") {
				t.Errorf("wait = %v", err)
			}
			rebooted := strings.Contains(strings.Join(r.calls, "\n"), "systemctl reboot --no-block")
			if rebooted != tc.reboot {
				t.Errorf("rebooted: %v, want %v", rebooted, tc.reboot)
			}
			record, _ := os.ReadFile(tc.h.Record)
			if lines := strings.Split(string(record), "\n"); len(lines) != 4 || lines[0] != "version 0.2.0" || lines[1] != "chalkd[1]: not healthy yet: chalkd does not serve" || !strings.HasPrefix(lines[2], "the node did not become healthy within 50ms: chalkd does not serve") {
				t.Errorf("record:\n%s", record)
			}
		})
	}
	r := &fakeRunner{}
	h := espWith(t, "chalkos_0.2.0.efi", "chalkos_0.1.0.efi", "chalkos_0.2.0+2-1.efi")
	h.Run, h.Chalkd = r, chalkdServing(t)
	if err := h.Wait(context.Background(), time.Minute); err != nil || len(r.calls) == 0 || strings.Contains(strings.Join(r.calls, "\n"), "reboot") {
		t.Errorf("a healthy boot: %v, %q", err, r.calls)
	}
	if _, err := os.Stat(h.Record); err == nil {
		t.Error("a healthy boot was recorded as failed")
	}
}

// TestHealthPresentsTheNodeCertificate checks that the health check completes chalkd's handshake
// with the node's certificate, so chalkd has no refusal to log every few seconds.
func TestHealthPresentsTheNodeCertificate(t *testing.T) {
	self, err := pki.SelfSigned("n1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "node.pem")
	write(t, path, self.Certificate+self.Key)
	presented := make(chan int, 1)
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
		presented <- len(raw)
		return nil
	}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	checkHealth(t, &Health{Run: &fakeRunner{}, Chalkd: srv.Listener.Addr().String(), NodeCertificate: path}, "")
	select {
	case n := <-presented:
		if n != 1 {
			t.Errorf("the check presented %d certificates", n)
		}
	case <-time.After(10 * time.Second):
		t.Error("chalkd never saw the node's certificate")
	}
}
