package chalkd

import (
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// nodeCertificateDir writes an installed node's chalkd directory with a certificate for n1.
func nodeCertificateDir(t *testing.T, c creds) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "chalkd")
	write(t, filepath.Join(dir, NodeCertificateFile), c.node.Certificate+c.node.Key)
	write(t, filepath.Join(dir, CAFile), c.ca.Certificate)
	return dir
}

// servedSerial connects to addr and returns the serial number of the certificate it serves.
func servedSerial(t *testing.T, addr string) string {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
}

func TestNodeCertificateSwitches(t *testing.T) {
	c := newCreds(t)
	dir := nodeCertificateDir(t, c)
	n, err := LoadNodeCertificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	addr := serveTLS(t, http.NotFoundHandler(), TLSConfig(n.GetCertificate, nil))
	first := servedSerial(t, addr)
	if first != n.Current().Leaf.SerialNumber.String() {
		t.Fatalf("serves %s, not the loaded certificate", first)
	}

	now := time.Now()
	renewed, err := pki.IssueNode(c.nodeCA, pki.NamesOf(n.Current().Leaf), now)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := n.Fingerprint()
	if err := n.Replace(renewed.Certificate, renewed.Key, now); err != nil {
		t.Fatal(err)
	}
	if got := servedSerial(t, addr); got == first || got != n.Current().Leaf.SerialNumber.String() {
		t.Errorf("serves %s after the replacement, want the new certificate", got)
	}
	if n.Fingerprint() == fingerprint {
		t.Error("the fingerprint did not change")
	}
	data, err := os.ReadFile(filepath.Join(dir, NodeCertificateFile))
	if err != nil || string(data) != renewed.Certificate+renewed.Key {
		t.Errorf("node.pem does not hold the new certificate and key: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(dir, NodeCertificateFile)); info.Mode().Perm() != 0o600 {
		t.Errorf("node.pem mode %v", info.Mode())
	}
	// A restarted chalkd serves what was written.
	again, err := LoadNodeCertificate(dir)
	if err != nil || again.Fingerprint() != n.Fingerprint() {
		t.Errorf("after a restart: %v", err)
	}
}

func TestNodeCertificateRefusesReplacements(t *testing.T) {
	// CAs from two years ago, so they issued an expired certificate then.
	now := time.Now()
	past := now.Add(-2 * pki.LeafValidity)
	c := newCredsAt(t, past)
	other := newCreds(t)
	n, err := LoadNodeCertificate(nodeCertificateDir(t, c))
	if err != nil {
		t.Fatal(err)
	}
	before := n.Fingerprint()
	otherNode, _ := pki.IssueNode(c.nodeCA, pki.NodeNames{CommonName: "n2"}, now)
	foreign, _ := pki.IssueNode(other.nodeCA, pki.NodeNames{CommonName: "n1"}, now)
	stale, err := pki.IssueNode(c.nodeCA, pki.NodeNames{CommonName: "n1"}, past)
	if err != nil {
		t.Fatal(err)
	}
	for name, ck := range map[string]pki.CertKey{
		"another node's":          otherNode,
		"of another OS CA":        foreign,
		"expired":                 stale,
		"with another node's key": {Certificate: c.node.Certificate, Key: otherNode.Key},
	} {
		err := n.Replace(ck.Certificate, ck.Key, now)
		if err == nil {
			t.Errorf("%s: replaced", name)
		} else if strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("%s: the error holds the key", name)
		}
	}
	if n.Fingerprint() != before {
		t.Error("a refused replacement changed the certificate")
	}
}

func TestLoadNodeCertificateKeepsAnExpiredOne(t *testing.T) {
	c := newCredsAt(t, time.Now().Add(-2*pki.LeafValidity))
	if cert, _, _ := c.node.Parse(); cert.NotAfter.After(time.Now()) {
		t.Fatal("the node certificate has not expired")
	}
	if _, err := LoadNodeCertificate(nodeCertificateDir(t, c)); err != nil {
		t.Errorf("an expired node certificate was not loaded, so chalkctl node renew could not reach the node: %v", err)
	}
	other := newCreds(t)
	c.node = other.node
	if _, err := LoadNodeCertificate(nodeCertificateDir(t, c)); err == nil {
		t.Error("loaded a node certificate of another OS CA")
	}
}

// A renewed chain arrives as PEM from another node; whatever its bytes, Replace must store a file
// the next start loads, or refuse it.
func TestNodeCertificateReplacementStaysLoadable(t *testing.T) {
	c := newCreds(t)
	other := newCreds(t)
	for name, edit := range map[string]func(chain string) string{
		"without a final newline":       func(chain string) string { return strings.TrimRight(chain, "\n") },
		"with another key in the chain": func(chain string) string { return chain + other.node.Key },
	} {
		t.Run(name, func(t *testing.T) {
			dir := nodeCertificateDir(t, c)
			n, err := LoadNodeCertificate(dir)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			renewed, err := pki.IssueNode(c.nodeCA, pki.NamesOf(n.Current().Leaf), now)
			if err != nil {
				t.Fatal(err)
			}
			if err := n.Replace(edit(renewed.Certificate), renewed.Key, now); err != nil {
				return
			}
			again, err := LoadNodeCertificate(dir)
			if err != nil {
				t.Fatalf("the replaced certificate cannot be loaded: %v", err)
			}
			if again.Fingerprint() != n.Fingerprint() {
				t.Error("a restart loads another certificate than the one served")
			}
		})
	}
}

func TestLoadNodeCertificateRemovesStaleTemporaryFiles(t *testing.T) {
	dir := nodeCertificateDir(t, newCreds(t))
	stale := filepath.Join(dir, "."+NodeCertificateFile+".123456")
	write(t, stale, "left by a crash")
	if _, err := LoadNodeCertificate(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a temporary file from an interrupted write is still there: %v", err)
	}
}
