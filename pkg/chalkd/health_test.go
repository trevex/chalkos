package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
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
	"github.com/trevex/chalkos/pkg/tool/tooltest"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// chalkdServing stands for chalkd: a TLS server that completes handshakes with the node's
// certificate, whose file it returns with its address.
func chalkdServing(t *testing.T) (addr, cert string) {
	t.Helper()
	self, err := pki.SelfSigned("n1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert = filepath.Join(t.TempDir(), "node.pem")
	write(t, cert, self.Certificate+self.Key)
	pair, err := tls.X509KeyPair([]byte(self.Certificate), []byte(self.Key))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), cert
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

// bootUnits is what systemctl lists of the units multi-user.target and sysinit.target pull in.
const bootUnits = "multi-user.target\n  chalkd.service\n  broken.service\n  timers.target\n  cleanup.timer\nsysinit.target\n  systemd-journald.service\n"

func TestHealthWithoutKubernetes(t *testing.T) {
	addr, cert := chalkdServing(t)
	other, _ := chalkdServing(t)
	units := tooltest.Rule{Prefix: "systemctl list-dependencies --all --plain --no-legend --no-pager multi-user.target sysinit.target", Out: bootUnits}
	failed := func(names ...string) tooltest.Rule {
		var out string
		for _, n := range names {
			out += n + " loaded failed failed Something\n"
		}
		return tooltest.Rule{Prefix: "systemctl list-units --state=failed", Out: out}
	}
	for _, tc := range []struct {
		name   string
		chalkd string
		rules  []tooltest.Rule
		ignore []string
		want   string
	}{
		{"healthy", addr, []tooltest.Rule{units}, nil, ""},
		{"chalkd not serving", "127.0.0.1:1", []tooltest.Rule{units}, nil, "chalkd does not serve"},
		{"another server than chalkd", other, []tooltest.Rule{units}, nil, "does not present the node's certificate"},
		{"config not applied", addr, []tooltest.Rule{{Prefix: "systemctl is-active --quiet chalkos-config.service", Err: errors.New("inactive")}, units}, nil, "the config was not applied"},
		{"a failed unit of the boot", addr, []tooltest.Rule{units, failed("broken.service", "cleanup.service")}, nil, "units failed: broken.service"},
		// A job a timer or a socket started is not part of the boot.
		{"a failed timer job", addr, []tooltest.Rule{units, failed("cleanup.service")}, nil, ""},
		{"a failed unit ignored", addr, []tooltest.Rule{units, failed("broken.service")}, []string{"broken.service"}, ""},
		{"the boot's units unknown", addr, []tooltest.Rule{{Prefix: "systemctl list-dependencies", Err: errors.New("exit status 1")}, failed("broken.service")}, nil, "the units of the boot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Health{Run: &tooltest.Runner{Rules: tc.rules}, Chalkd: tc.chalkd, NodeCertificate: cert, IgnoreUnits: tc.ignore}
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
	addr, cert := chalkdServing(t)
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
			h := &Health{Run: r, Kubernetes: s.Kubernetes, Chalkd: addr, NodeCertificate: cert, Kubelet: kubelet(t, &healthy)}
			checkHealth(t, h, tc.want)
		})
	}
	s, r := kubernetesServer(t, k8s.KindWorker, true)
	withoutAddress(t, s, "no ipv4 node address matches validSubnets 10.0.0.0/8")
	healthy := true
	checkHealth(t, &Health{Run: r, Kubernetes: s.Kubernetes, Chalkd: addr, NodeCertificate: cert, Kubelet: kubelet(t, &healthy)}, "preparation failed: no ipv4 node address")
}

func TestHealthOfAControlPlane(t *testing.T) {
	addr, cert := chalkdServing(t)
	s, _, members := memberServer(t)
	k := s.Kubernetes
	ready, healthy := true, true
	k.APIServerReady = func(context.Context, k8s.Cluster, kpki.Share) bool { return ready }
	h := &Health{Run: &tooltest.Runner{}, Kubernetes: k, Chalkd: addr, NodeCertificate: cert, Kubelet: kubelet(t, &healthy)}
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
	checkHealth(t, &Health{Run: r, Kubernetes: s2.Kubernetes, Chalkd: addr, NodeCertificate: cert, Kubelet: "http://127.0.0.1:1/healthz"}, "")
}

// espWith is a health check of a node whose ESP holds the UKIs; see writeESP.
func espWith(t *testing.T, booted string, files ...string) *Health {
	t.Helper()
	root := t.TempDir()
	h := &Health{ESP: filepath.Join(root, "esp"), EFIVars: filepath.Join(root, "efivars"), Cmdline: filepath.Join(root, "cmdline"),
		Record: filepath.Join(root, "var", "failed-boot")}
	writeESP(t, h.ESP, h.EFIVars, h.Cmdline, booted, files...)
	return h
}

// writeESP writes UKIs to an ESP, chalkos_<generation>.efi with or without a tries counter, each
// booting the store whose root hash repeats the generation's digit in ASCII, 0x32 for generation
// 2, and marks the named one as the entry the node booted from that store.
func writeESP(t *testing.T, esp, efivars, cmdline, booted string, files ...string) {
	t.Helper()
	hash := func(file string) string {
		return strings.Repeat(fmt.Sprintf("%02x", strings.TrimPrefix(file, "chalkos_")[0]), 32)
	}
	for _, f := range files {
		write(t, filepath.Join(esp, "EFI", "Linux", f), string(ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos", "IMAGE_VERSION": ""}, "usrhash="+hash(f))))
	}
	value := []byte{7, 0, 0, 0}
	for _, u := range utf16.Encode([]rune(booted + "\x00")) {
		value = binary.LittleEndian.AppendUint16(value, u)
	}
	write(t, filepath.Join(efivars, "LoaderEntrySelected-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"), string(value))
	write(t, cmdline, "init=/x usrhash="+hash(booted)+"\n")
}

// journalRule is the journal of a boot that never became healthy.
var journalRule = tooltest.Rule{Prefix: "journalctl --boot --no-pager --output=short-iso --lines=30 --unit=chalkd.service --unit=chalkos-health.service", Out: "chalkd[1]: not healthy yet: chalkd does not serve\n"}

// failedHeader is the first line of a record of a failed boot of generation 2, as writeESP builds
// it.
var failedHeader = "usrhash=" + strings.Repeat("32", 32) + " generation=2"

// TestHealthWaitReboots waits for a boot that never becomes healthy, records its journal on VAR, and
// reboots only while the boot loader counts it and has somewhere to go.
func TestHealthWaitReboots(t *testing.T) {
	_, cert := chalkdServing(t)
	for _, tc := range []struct {
		name   string
		h      *Health
		reboot bool
	}{
		{"a try with tries left", espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+2-1.efi"), true},
		{"the last try", espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+0-3.efi"), true},
		{"a blessed boot", espWith(t, "chalkos_2.efi", "chalkos_1+0-3.efi", "chalkos_2.efi"), false},
		{"nothing to fall back to", espWith(t, "chalkos_2.efi", "chalkos_2+0-4.efi"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &tooltest.Runner{Rules: []tooltest.Rule{journalRule}}
			tc.h.Run, tc.h.Chalkd, tc.h.NodeCertificate, tc.h.Interval = r, "127.0.0.1:1", cert, 10*time.Millisecond
			err := tc.h.Wait(context.Background(), 50*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), "did not become healthy") {
				t.Errorf("wait = %v", err)
			}
			rebooted := strings.Contains(strings.Join(r.Calls, "\n"), "systemctl reboot --no-block")
			if rebooted != tc.reboot {
				t.Errorf("rebooted: %v, want %v", rebooted, tc.reboot)
			}
			record, _ := os.ReadFile(tc.h.Record)
			if lines := strings.Split(string(record), "\n"); len(lines) != 4 || lines[0] != failedHeader || lines[1] != "chalkd[1]: not healthy yet: chalkd does not serve" || !strings.HasPrefix(lines[2], "the node did not become healthy within 50ms: chalkd does not serve") {
				t.Errorf("record:\n%s", record)
			}
		})
	}
}

// TestHealthWaitHealthy finds a boot healthy without rebooting, and forgets the record of a
// failed boot once a boot the loader counts was found healthy: that image boots for good now.
func TestHealthWaitHealthy(t *testing.T) {
	addr, cert := chalkdServing(t)
	for _, tc := range []struct {
		name   string
		h      *Health
		forget bool
	}{
		{"a counted boot", espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+2-1.efi"), true},
		{"a blessed boot", espWith(t, "chalkos_2.efi", "chalkos_3+0-3.efi", "chalkos_2.efi"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &tooltest.Runner{}
			tc.h.Run, tc.h.Chalkd, tc.h.NodeCertificate = r, addr, cert
			write(t, tc.h.Record, "usrhash="+strings.Repeat("33", 32)+" generation=3\nchalkd[1]: not healthy yet\n")
			if err := tc.h.Wait(context.Background(), time.Minute); err != nil || len(r.Calls) == 0 || strings.Contains(strings.Join(r.Calls, "\n"), "reboot") {
				t.Errorf("a healthy boot: %v, %q", err, r.Calls)
			}
			if _, err := os.Stat(tc.h.Record); errors.Is(err, fs.ErrNotExist) != tc.forget {
				t.Errorf("the record of the failed boot: %v, want it forgotten: %v", err, tc.forget)
			}
		})
	}
}

// TestHealthWaitEndsAtTheDeadline gives up once the timeout passed, also while a check hangs:
// systemd's own timeout must not stop the health check before it decided.
func TestHealthWaitEndsAtTheDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	// A server that accepts connections and never answers a handshake.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()
	h := espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+2-1.efi")
	r := &tooltest.Runner{Rules: []tooltest.Rule{journalRule}}
	_, cert := chalkdServing(t)
	h.Run, h.Chalkd, h.NodeCertificate = r, ln.Addr().String(), cert
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- h.Wait(context.Background(), 300*time.Millisecond) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not become healthy") {
			t.Errorf("wait = %v", err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("the wait of 300ms took %v", took)
		}
		if !strings.Contains(strings.Join(r.Calls, "\n"), "systemctl reboot --no-block") {
			t.Error("the node was not rebooted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait of 300ms still runs after 10s")
	}
}

// TestHealthStopped reboots a counted boot whose health check ended before it decided, stopped by
// systemd or crashed, as Wait would have, and nothing after a check that decided.
func TestHealthStopped(t *testing.T) {
	for _, tc := range []struct {
		name           string
		h              *Health
		result, status string
		reboot         bool
	}{
		{"a check that decided", espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+2-1.efi"), "exit-code", "1", false},
		{"a healthy boot", espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+2-1.efi"), "success", "0", false},
		{"a check timed out", espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+2-1.efi"), "timeout", "1", true},
		{"a check killed", espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+0-3.efi"), "signal", "KILL", true},
		{"a check that crashed", espWith(t, "chalkos_2.efi", "chalkos_1.efi", "chalkos_2+2-1.efi"), "exit-code", "2", true},
		{"a check timed out with nothing to fall back to", espWith(t, "chalkos_2.efi", "chalkos_2+0-4.efi"), "timeout", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &tooltest.Runner{Rules: []tooltest.Rule{journalRule}}
			tc.h.Run = r
			tc.h.Stopped(context.Background(), tc.result, tc.status)
			if rebooted := strings.Contains(strings.Join(r.Calls, "\n"), "systemctl reboot --no-block"); rebooted != tc.reboot {
				t.Errorf("rebooted: %v, want %v", rebooted, tc.reboot)
			}
			record, _ := os.ReadFile(tc.h.Record)
			if decided := tc.status == "1" && tc.result == "exit-code" || tc.result == "success"; decided != (len(record) == 0) {
				t.Errorf("record:\n%s", record)
			}
		})
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
	pair, err := tls.X509KeyPair([]byte(self.Certificate), []byte(self.Key))
	if err != nil {
		t.Fatal(err)
	}
	presented := make(chan int, 1)
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAnyClientCert, VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
		presented <- len(raw)
		return nil
	}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	checkHealth(t, &Health{Run: &tooltest.Runner{}, Chalkd: srv.Listener.Addr().String(), NodeCertificate: path}, "")
	select {
	case n := <-presented:
		if n != 1 {
			t.Errorf("the check presented %d certificates", n)
		}
	case <-time.After(10 * time.Second):
		t.Error("chalkd never saw the node's certificate")
	}
}
