package pki

import (
	"crypto/x509"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func newTestCA(t *testing.T) CertKey {
	t.Helper()
	ca, err := NewCA("chalkos OS CA", now)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func pool(t *testing.T, ca CertKey) *x509.CertPool {
	t.Helper()
	cert, err := ParseCertificate([]byte(ca.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	p := x509.NewCertPool()
	p.AddCert(cert)
	return p
}

func TestNewCA(t *testing.T) {
	ca := newTestCA(t)
	cert, _, err := ca.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if !cert.IsCA || cert.Subject.CommonName != "chalkos OS CA" {
		t.Errorf("CA = %+v", cert.Subject)
	}
	if got := cert.NotAfter.Sub(now); got != CAValidity {
		t.Errorf("CA valid for %v after now, want %v", got, CAValidity)
	}
}

func TestIssueClient(t *testing.T) {
	ca := newTestCA(t)
	client, err := IssueClient(ca, "admin", RoleAdmin, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, _, err := client.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool(t, ca), CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("client certificate does not verify: %v", err)
	}
	if role, ok := Role(cert); !ok || role != RoleAdmin {
		t.Errorf("role = %q, %v; want admin", role, ok)
	}
	if got := cert.NotAfter.Sub(now); got != LeafValidity {
		t.Errorf("client valid for %v after now, want %v", got, LeafValidity)
	}
	if _, err := IssueClient(ca, "x", "root", now); err == nil {
		t.Error("issued a certificate for an unknown role")
	}
}

func TestIssueNode(t *testing.T) {
	ca := newTestCA(t)
	node, err := IssueNode(ca, "w1", []string{"w1", "w1.lan"}, []net.IP{net.ParseIP("10.0.0.21")}, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, _, err := node.Parse()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"w1", "w1.lan", "10.0.0.21"} {
		if err := cert.VerifyHostname(name); err != nil {
			t.Errorf("node certificate not valid for %s: %v", name, err)
		}
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool(t, ca), CurrentTime: now, DNSName: "w1"}); err != nil {
		t.Errorf("node certificate does not verify as a server: %v", err)
	}
	if _, ok := Role(cert); ok {
		t.Error("a node certificate grants a client role")
	}
}

func TestSelfSigned(t *testing.T) {
	c, err := SelfSigned("chalkd", now)
	if err != nil {
		t.Fatal(err)
	}
	cert, _, err := c.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if fp := Fingerprint(cert.Raw); !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fp) {
		t.Errorf("fingerprint = %q", fp)
	}
}

func TestParseRejectsForeignKey(t *testing.T) {
	a, b := newTestCA(t), newTestCA(t)
	if _, _, err := (CertKey{Certificate: a.Certificate, Key: b.Key}).Parse(); err == nil {
		t.Error("accepted a key that belongs to another certificate")
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	want := "ab01cd"
	for _, in := range []string{"ab01cd", "AB:01:CD", " sha256:ab 01 cd ", "SHA256:AB01CD"} {
		if got := NormalizeFingerprint(in); got != want {
			t.Errorf("NormalizeFingerprint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAllows(t *testing.T) {
	for _, c := range []struct {
		role, need string
		want       bool
	}{
		{RoleAdmin, RoleAdmin, true},
		{RoleAdmin, RoleReader, true},
		{RoleOperator, RoleOperator, true},
		{RoleOperator, RoleAdmin, false},
		{RoleReader, RoleReader, true},
		{RoleReader, RoleOperator, false},
		{RoleNode, RoleReader, false},
		{"", RoleReader, false},
	} {
		if got := Allows(c.role, c.need); got != c.want {
			t.Errorf("Allows(%q, %q) = %v, want %v", c.role, c.need, got, c.want)
		}
	}
}

func TestRecoveryKey(t *testing.T) {
	secret := []byte(strings.Repeat("s", RecoverySecretSize))
	key, err := RecoveryKey(secret, "homelab", "w1")
	if err != nil {
		t.Fatal(err)
	}
	// The shape of `systemd-cryptenroll --recovery-key` output.
	if !regexp.MustCompile(`^([cbdefghijklnrtuv]{8}-){7}[cbdefghijklnrtuv]{8}$`).MatchString(key) {
		t.Errorf("recovery key %q is not formatted like a systemd recovery key", key)
	}
	if want := "tlvujiln-vffneuhk-idrhucle-evlgjnvd-ljvggbvr-djbthrbc-cgfrvhcb-vucrunlc"; key != want {
		t.Errorf("recovery key = %q, want %q", key, want)
	}
	again, _ := RecoveryKey(secret, "homelab", "w1")
	other, _ := RecoveryKey(secret, "homelab", "w2")
	otherCluster, _ := RecoveryKey(secret, "lab", "w1")
	if again != key || other == key || otherCluster == key {
		t.Error("recovery keys must be stable per node and differ between nodes and clusters")
	}
	if _, err := RecoveryKey(secret[:16], "homelab", "w1"); err == nil {
		t.Error("accepted a short secret")
	}
}
