package chalkd

import (
	"context"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage/node"
)

func status(t *testing.T, s *Server) *nodev1.StatusResponse {
	t.Helper()
	resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg
}

func certificateNames(list []*nodev1.CertificateStatus) []string {
	var names []string
	for _, c := range list {
		names = append(names, c.Name)
	}
	return names
}

func TestStatusCertificatesOfAControlPlane(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	withNodeCertificate(t, s)
	st := status(t, s)
	want := []string{"node", "OS CA", "node CA", "Kubernetes CA", "front-proxy CA", "etcd CA", "Kubernetes control plane", "kubelet client"}
	if got := certificateNames(st.Certificates); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("certificates %v, want %v", got, want)
	}
	for _, c := range st.Certificates {
		if c.Problem != "" || !c.NotAfter.AsTime().After(time.Now()) {
			t.Errorf("%s: expires %v, problem %q", c.Name, c.NotAfter.AsTime(), c.Problem)
		}
	}

	// A failing renewal shows with the certificate it renews.
	s.Renewal = NewNodeRenewal(s.Certificate, nil)
	s.Renewal.setProblem("renewal failing: connection refused; expires 2027-10-08T12:00:00Z")
	if got := status(t, s).Certificates[0]; got.Problem != "renewal failing: connection refused; expires 2027-10-08T12:00:00Z" {
		t.Errorf("node certificate problem %q", got.Problem)
	}
}

func TestStatusCertificatesOfAWorker(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindWorker, true)
	withNodeCertificate(t, s)
	want := []string{"node", "OS CA", "Kubernetes CA", "kubelet client"}
	if got := certificateNames(status(t, s).Certificates); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("certificates %v, want %v", got, want)
	}

	// VAR lost the kubelet's renewed certificate and the share's expired too.
	past := time.Now().Add(-2 * pki.LeafValidity)
	k, err := pki.NewKubernetesSecrets(past)
	if err != nil {
		t.Fatal(err)
	}
	share, err := kpki.WorkerShare(k, "n1", past)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := share.Encode()
	write(t, s.Kubernetes.Paths.Share(), string(data))
	if err := os.Remove(s.Kubernetes.Paths.KubeletClient()); err != nil {
		t.Fatal(err)
	}
	var kubelet *nodev1.CertificateStatus
	for _, c := range status(t, s).Certificates {
		if c.Name == "kubelet client" {
			kubelet = c
		}
	}
	if kubelet == nil || !strings.Contains(kubelet.Problem, "chalkctl apply-identity n1 --kubernetes-share") {
		t.Errorf("kubelet client: %v, want the command that delivers a new one", kubelet)
	}
}

func TestStatusCertificatesWithoutKubernetes(t *testing.T) {
	s, _ := installedServer(t, section("", ""), false)
	withNodeCertificate(t, s)
	st := status(t, s)
	if got := certificateNames(st.Certificates); strings.Join(got, ",") != "node,OS CA" {
		t.Errorf("certificates %v", got)
	}
}

// A node without Kubernetes renews its node certificate only when told to, so the status names
// the command with the node's name.
func TestStatusNamesTheNodeToRenew(t *testing.T) {
	s, _ := installedServer(t, section("", ""), false)
	withNodeCertificate(t, s)
	want := "less than a third of its lifetime remains; renew it with chalkctl node renew n1"
	if got := s.certificates(time.Now().Add(pki.LeafValidity * 3 / 4))[0]; got.Name != "node" || got.Problem != want {
		t.Errorf("%s: problem %q, want %q", got.Name, got.Problem, want)
	}
}

func TestCertificateProblems(t *testing.T) {
	now := time.Now()
	cert := func(from, until time.Duration) *x509.Certificate {
		return &x509.Certificate{NotBefore: now.Add(from), NotAfter: now.Add(until)}
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"leaf early", leafProblem(cert(-time.Hour, 300*24*time.Hour), now), ""},
		{"leaf in its last third", leafProblem(cert(-250*24*time.Hour, 100*24*time.Hour), now), "less than a third of its lifetime remains"},
		{"leaf expired", leafProblem(cert(-400*24*time.Hour, -time.Hour), now), "expired"},
		{"CA with years left", caProblem(cert(0, 2*caWarning), now, caWarning), ""},
		{"CA within its warning", caProblem(cert(0, caWarning-time.Hour), now, caWarning), "expires " + now.Add(caWarning-time.Hour).UTC().Format(time.DateOnly)},
		{"node CA within 18 months", caProblem(cert(0, 500*24*time.Hour), now, nodeCAWarning), "expires " + now.Add(500*24*time.Hour).UTC().Format(time.DateOnly)},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestParseTracking(t *testing.T) {
	synced, err := parseTracking([]byte("C0A80001,10.0.2.2,2,1791460000.123456789,0.000012345,0.000001000,0.000020000,-12.345,0.001,0.020,0.010000000,0.001000000,64.4,Normal\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !synced.Synchronised || synced.Source != "10.0.2.2" || synced.OffsetSeconds != -0.000012345 {
		t.Errorf("synchronised: %+v", synced)
	}
	unsynced, err := parseTracking([]byte("00000000,,0,0.000000000,0.000000000,0.000000000,0.000000000,0.000,0.000,0.000,1.000000000,1.000000000,0.0,Not synchronised\n"))
	if err != nil {
		t.Fatal(err)
	}
	if unsynced.Synchronised || unsynced.Source != "" {
		t.Errorf("not synchronised: %+v", unsynced)
	}
	if _, err := parseTracking([]byte("506 Cannot talk to daemon\n")); err == nil {
		t.Error("parsed chronyc's error")
	}
}

func TestStatusTime(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	r.rules = append(r.rules, rule{prefix: "chronyc -n -c tracking", out: "C0A80001,10.0.2.2,2,1791460000.1,-0.5,0,0,0,0,0,0,0,64,Normal\n"})
	if st := status(t, s).Time; !st.Synchronised || st.Source != "10.0.2.2" || st.OffsetSeconds != 0.5 || st.Error != "" {
		t.Errorf("time = %+v", st)
	}
}

// kubeletStatus returns the status entry of the kubelet's client certificate.
func kubeletStatus(t *testing.T, s *Server) *nodev1.CertificateStatus {
	t.Helper()
	for _, c := range status(t, s).Certificates {
		if c.Name == "kubelet client" {
			return c
		}
	}
	t.Fatal("no kubelet client certificate in the status")
	return nil
}

// The kubelet renews its certificates itself once 70 to 90 % of their lifetime passed; status
// warns only when it fell behind.
func TestStatusKubeletCertificatesWarnLate(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	// A CA from before the certificates it issues, so they keep their dates.
	k, err := pki.NewKubernetesSecrets(time.Now().Add(-2 * pki.LeafValidity))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		issued time.Duration
		warn   bool
	}{
		{-250 * 24 * time.Hour, false},
		{-350 * 24 * time.Hour, true},
	} {
		ck, err := kpki.IssueKubeletClient(k.CA, "n1", time.Now().Add(tc.issued))
		if err != nil {
			t.Fatal(err)
		}
		write(t, s.Kubernetes.Paths.KubeletClient(), ck.Certificate+ck.Key)
		if got := kubeletStatus(t, s).Problem; (got != "") != tc.warn {
			t.Errorf("issued %v ago: problem %q, want a warning %v", -tc.issued, got, tc.warn)
		}
	}
}

func TestStatusNamesTheOSCA(t *testing.T) {
	s, _ := installedServer(t, section("", ""), false)
	// Issued so long ago that it expires within the year; the node trusts it besides its own.
	osCA, err := pki.NewOSCA(time.Now().Add(-pki.CAValidity + 100*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	own, _ := testOSCA()
	write(t, filepath.Join(s.Paths.StateDir, "chalkd", CAFile), pki.Bundle(own.Certificate, osCA.Certificate))
	withNodeCertificate(t, s)
	expiring, _ := pki.ParseCertificate([]byte(osCA.Certificate))
	var named bool
	for _, c := range status(t, s).Certificates {
		if c.Name == "OS CA" && c.Fingerprint == pki.Fingerprint(expiring.Raw) {
			named = strings.HasPrefix(c.Problem, "OS CA expires ")
		}
	}
	if !named {
		t.Error("the expiring OS CA's problem does not name it as the other CAs' do")
	}
}

// A share chalkd cannot read leaves the Kubernetes certificates unknown, which status says.
func TestStatusReportsAnUnreadableShare(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	write(t, s.Kubernetes.Paths.Share(), "not a share")
	var found bool
	for _, c := range status(t, s).Certificates {
		if strings.Contains(c.Problem, "share") {
			found = true
		}
	}
	if !found {
		t.Errorf("certificates %v, want the unreadable share", certificateNames(status(t, s).Certificates))
	}
}

// chronyc fails on every status while chronyd is down; its standard error shows once, in the
// status, and not in chalkd's log on each call.
func TestStatusTimeWhileChronydIsDown(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	r.rules = append(r.rules, rule{prefix: "chronyc -n -c tracking", err: &node.ToolError{Command: "chronyc -n -c tracking", Code: 1, Stderr: "506 Cannot talk to daemon\n"}})
	st := status(t, s).Time
	if strings.Count(st.Error, "506 Cannot talk to daemon") != 1 {
		t.Errorf("time error %q, want chronyc's message once", st.Error)
	}
	if !r.quiet["chronyc -n -c tracking"] {
		t.Error("chronyc's standard error went to chalkd's log")
	}
}

func TestParseTrackingReadsTheLeapStatusField(t *testing.T) {
	// A later chrony may add fields after the leap status.
	st, err := parseTracking([]byte("C0A80001,10.0.2.2,2,1791460000.1,0,0,0,0,0,0,0,0,64,Not synchronised,0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Synchronised {
		t.Errorf("synchronised with leap status %q", "Not synchronised")
	}
}
