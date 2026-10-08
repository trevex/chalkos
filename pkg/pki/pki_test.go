package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
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
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool(t, ca), CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Error("node certificate verifies as a client; node certificates are server certificates only")
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
		{RoleReader, RoleNode, false},
		{RoleReader, "", false},
		{RoleAdmin, "admn", false},
		{RoleNode, RoleNode, true},
		{RoleAdmin, RoleNode, false},
		{"", RoleNode, false},
		{RoleNode, RoleAdmin, false},
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
	if _, err := RecoveryKey(secret, "a\x00b", "c"); err == nil {
		t.Error("accepted a cluster name containing a NUL byte")
	}
}

func TestIssueLeaf(t *testing.T) {
	ca := newTestCA(t)
	ck, err := IssueLeaf(ca, Leaf{
		CommonName:   "system:node:w1",
		Organization: []string{"system:nodes"},
		DNSNames:     []string{"w1"},
		IPs:          []net.IP{net.ParseIP("10.0.0.21")},
		Server:       true,
		Client:       true,
		Validity:     time.Hour,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, _, err := ck.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool(t, ca), CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("does not verify against the CA: %v", err)
	}
	if cert.Subject.CommonName != "system:node:w1" || strings.Join(cert.Subject.Organization, ",") != "system:nodes" ||
		strings.Join(cert.DNSNames, ",") != "w1" || len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("10.0.0.21")) {
		t.Errorf("subject %v, DNS %v, IPs %v", cert.Subject, cert.DNSNames, cert.IPAddresses)
	}
	if len(cert.ExtKeyUsage) != 2 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || cert.ExtKeyUsage[1] != x509.ExtKeyUsageClientAuth {
		t.Errorf("extended key usages %v", cert.ExtKeyUsage)
	}
	if !cert.NotAfter.Equal(now.Add(time.Hour).Truncate(time.Second)) {
		t.Errorf("expires %v", cert.NotAfter)
	}
	if _, err := IssueLeaf(ca, Leaf{CommonName: "x"}, now); err == nil {
		t.Error("issued a certificate without a usage")
	}
}

func TestLeafDoesNotPredateCA(t *testing.T) {
	ca := newTestCA(t)
	// A CA made just now, as openssl makes one without backdating.
	caCert, _, _ := ca.Parse()
	leaf, err := IssueLeaf(ca, Leaf{CommonName: "early", Client: true}, caCert.NotBefore)
	if err != nil {
		t.Fatal(err)
	}
	cert, _, _ := leaf.Parse()
	if cert.NotBefore.Before(caCert.NotBefore) {
		t.Errorf("leaf starts %v, before its CA at %v", cert.NotBefore, caCert.NotBefore)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool(t, ca), CurrentTime: cert.NotBefore, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("does not verify at its start: %v", err)
	}
}

func TestValidateCARequiresCertSign(t *testing.T) {
	if err := ValidateCA(caWithoutCertSign(t)); err == nil {
		t.Error("accepted a CA certificate without the certificate signing key usage")
	}
	if err := ValidateCA(newTestCA(t)); err != nil {
		t.Errorf("rejected a CA: %v", err)
	}
}

// caWithoutCertSign is a CA certificate that lacks the certificate signing key usage.
func caWithoutCertSign(t *testing.T) CertKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template, err := newTemplate("no cert sign", nil, now, CAValidity)
	if err != nil {
		t.Fatal(err)
	}
	template.IsCA = true
	template.BasicConstraintsValid = true
	template.KeyUsage = x509.KeyUsageCRLSign
	ca, err := sign(template, template, key, key)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestIssueFromExpiredCA(t *testing.T) {
	for name, created := range map[string]time.Time{
		"expired a day ago":        now.Add(-CAValidity - 24*time.Hour),
		"expired half an hour ago": now.Add(-CAValidity - 30*time.Minute),
	} {
		ca, err := NewCA("old CA", created)
		if err != nil {
			t.Fatal(err)
		}
		caCert, _, _ := ca.Parse()
		_, err = IssueLeaf(ca, Leaf{CommonName: "late", Client: true}, now)
		if err == nil {
			t.Errorf("%s: issued a certificate", name)
			continue
		}
		if !strings.Contains(err.Error(), caCert.NotAfter.UTC().Format(time.RFC3339)) {
			t.Errorf("%s: error %q does not name the CA's expiry", name, err)
		}
		if strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("%s: error contains a key", name)
		}
	}
}

// newTestNodeCA returns an OS CA and a node CA it issued.
func newTestNodeCA(t *testing.T) (osCA, nodeCA CertKey) {
	t.Helper()
	osCA, err := NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	if nodeCA, err = NewNodeCA(osCA, now); err != nil {
		t.Fatal(err)
	}
	return osCA, nodeCA
}

// signWith issues a certificate from ca as the template says, bypassing the checks of the
// package's issuers, as a holder of ca's key could.
func signWith(t *testing.T, ca CertKey, template *x509.Certificate) CertKey {
	t.Helper()
	full, err := newTemplate(template.Subject.CommonName, template.Subject.Organization, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	full.ExtKeyUsage, full.KeyUsage = template.ExtKeyUsage, template.KeyUsage|x509.KeyUsageDigitalSignature
	full.IsCA, full.BasicConstraintsValid, full.MaxPathLenZero = template.IsCA, template.BasicConstraintsValid, template.MaxPathLenZero
	ck, err := issue(ca, full, now)
	if err != nil {
		t.Fatal(err)
	}
	return ck
}

func verifyChain(t *testing.T, osCA CertKey, leaf CertKey, intermediates []CertKey, usage x509.ExtKeyUsage) ([][]*x509.Certificate, error) {
	t.Helper()
	cert, err := ParseCertificate([]byte(leaf.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	inter := x509.NewCertPool()
	for _, i := range intermediates {
		c, err := ParseCertificate([]byte(i.Certificate))
		if err != nil {
			t.Fatal(err)
		}
		inter.AddCert(c)
	}
	return cert.Verify(x509.VerifyOptions{Roots: pool(t, osCA), Intermediates: inter, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{usage}})
}

func TestNodeCA(t *testing.T) {
	osCA, nodeCA := newTestNodeCA(t)
	cert, _, err := nodeCA.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if !IsNodeCA(cert) || cert.Subject.CommonName != "chalkos node CA" {
		t.Errorf("node CA = %s, IsNodeCA %v", cert.Subject, IsNodeCA(cert))
	}
	if got := cert.NotAfter.Sub(now); got != NodeCAValidity {
		t.Errorf("node CA valid for %v after now, want %v", got, NodeCAValidity)
	}
	if root, _ := ParseCertificate([]byte(osCA.Certificate)); IsNodeCA(root) {
		t.Error("the OS CA counts as a node CA")
	}

	// The OS CA's own end is the node CA's: it never outlives the root.
	late := now.Add(CAValidity - 24*time.Hour)
	capped, err := NewNodeCA(osCA, late)
	if err != nil {
		t.Fatal(err)
	}
	cappedCert, _ := ParseCertificate([]byte(capped.Certificate))
	root, _ := ParseCertificate([]byte(osCA.Certificate))
	if !cappedCert.NotAfter.Equal(root.NotAfter) {
		t.Errorf("node CA issued a day before the OS CA expires ends %v, want the OS CA's %v", cappedCert.NotAfter, root.NotAfter)
	}

	// A Kubernetes CA issues leaves only, so it cannot issue a node CA.
	k8sCA := newTestCA(t)
	sub, err := NewNodeCA(k8sCA, now)
	if err != nil {
		t.Fatal(err)
	}
	leaf := signWith(t, sub, &x509.Certificate{Subject: pkix.Name{CommonName: "n1"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	if _, err := verifyChain(t, k8sCA, leaf, []CertKey{sub}, x509.ExtKeyUsageServerAuth); err == nil {
		t.Error("a leaf CA's intermediate verifies")
	}
}

// TestNodeCAKeyUsages checks that whatever the node CA's key signs verifies for TLS servers and
// clients alone, and that it cannot create a CA of its own.
func TestNodeCAKeyUsages(t *testing.T) {
	osCA, nodeCA := newTestNodeCA(t)
	for _, tc := range []struct {
		name  string
		leaf  []x509.ExtKeyUsage
		usage x509.ExtKeyUsage
		ok    bool
	}{
		{"server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, x509.ExtKeyUsageServerAuth, true},
		{"client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, x509.ExtKeyUsageClientAuth, true},
		{"code signing", []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}, x509.ExtKeyUsageCodeSigning, false},
		{"email", []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}, x509.ExtKeyUsageEmailProtection, false},
		{"any usage asked as code signing", []x509.ExtKeyUsage{x509.ExtKeyUsageAny}, x509.ExtKeyUsageCodeSigning, false},
		{"no usage asked as time stamping", nil, x509.ExtKeyUsageTimeStamping, false},
	} {
		leaf := signWith(t, nodeCA, &x509.Certificate{Subject: pkix.Name{CommonName: "n1"}, ExtKeyUsage: tc.leaf})
		_, err := verifyChain(t, osCA, leaf, []CertKey{nodeCA}, tc.usage)
		if (err == nil) != tc.ok {
			t.Errorf("%s: verify = %v, want ok %v", tc.name, err, tc.ok)
		}
	}

	sub := signWith(t, nodeCA, &x509.Certificate{Subject: pkix.Name{CommonName: "sub CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign})
	leaf := signWith(t, sub, &x509.Certificate{Subject: pkix.Name{CommonName: "n1"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if _, err := verifyChain(t, osCA, leaf, []CertKey{nodeCA, sub}, x509.ExtKeyUsageClientAuth); err == nil {
		t.Error("a certificate of a CA the node CA issued verifies")
	}
}

func TestClientRole(t *testing.T) {
	osCA, nodeCA := newTestNodeCA(t)
	for _, tc := range []struct {
		name         string
		ca           CertKey
		organization []string
		role         string
		ok           bool
	}{
		{"admin from the OS CA", osCA, []string{RoleAdmin}, RoleAdmin, true},
		{"reader from the OS CA", osCA, []string{RoleReader}, RoleReader, true},
		{"node organization from the OS CA", osCA, []string{RoleNode}, "", false},
		{"no organization from the OS CA", osCA, nil, "", false},
		{"admin from the node CA", nodeCA, []string{RoleAdmin}, RoleNode, true},
		{"operator and reader from the node CA", nodeCA, []string{RoleOperator, RoleReader}, RoleNode, true},
		{"no organization from the node CA", nodeCA, nil, RoleNode, true},
	} {
		leaf := signWith(t, tc.ca, &x509.Certificate{Subject: pkix.Name{CommonName: "c", Organization: tc.organization}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
		chains, err := verifyChain(t, osCA, leaf, []CertKey{nodeCA}, x509.ExtKeyUsageClientAuth)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if role, ok := ClientRole(chains); role != tc.role || ok != tc.ok {
			t.Errorf("%s: role %q, %v; want %q, %v", tc.name, role, ok, tc.role, tc.ok)
		}
	}
	if _, ok := ClientRole(nil); ok {
		t.Error("a client without a verified chain has a role")
	}
}
