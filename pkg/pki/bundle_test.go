package pki

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseBundle(t *testing.T) {
	a, b := newTestCA(t), newTestCA(t)
	certs, err := ParseBundle(Bundle(a.Certificate, b.Certificate, a.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := ParseCertificate([]byte(a.Certificate))
	second, _ := ParseCertificate([]byte(b.Certificate))
	if len(certs) != 2 || !certs[0].Equal(first) || !certs[1].Equal(second) {
		t.Errorf("parsed %d certificates, want a's and then b's", len(certs))
	}
	fps, err := Fingerprints(Bundle(b.Certificate, a.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fps, []string{Fingerprint(second.Raw), Fingerprint(first.Raw)}) {
		t.Errorf("fingerprints %v are not b's and a's", fps)
	}
	for name, bundle := range map[string]string{
		"empty":                         "",
		"a key":                         a.Certificate + a.Key,
		"text after":                    a.Certificate + "garbage",
		"text before":                   "garbage\n" + a.Certificate,
		"text between":                  a.Certificate + "garbage\n" + b.Certificate,
		"a broken block before another": "-----BEGIN CERTIFICATE-----\n" + a.Certificate,
		"a broken block":                "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
		"whitespace only":               "\n \n",
	} {
		_, err := ParseBundle(bundle)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "BEGIN") {
			t.Errorf("%s: the error echoes the bundle: %v", name, err)
		}
	}
}

// TestVerifyNodeAgainstBundle checks that a node certificate verifies against a bundle holding its
// OS CA among others, as nodes trust the OS CAs while one rotates.
func TestVerifyNodeAgainstBundle(t *testing.T) {
	osCA, nodeCA := newTestNodeCA(t)
	otherOS, otherNodeCA := newTestNodeCA(t)
	node, err := IssueNode(nodeCA, NodeNames{CommonName: "w1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, bundle := range map[string]string{
		"its OS CA first":  Bundle(osCA.Certificate, otherOS.Certificate),
		"its OS CA second": Bundle(otherOS.Certificate, osCA.Certificate),
	} {
		cred, err := VerifyNode(node.Certificate, node.Key, bundle, now)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if _, err := NodeFile(cred, bundle); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if err := ValidateNodeCA(nodeCA, bundle); err != nil {
			t.Errorf("%s: the node CA: %v", name, err)
		}
	}
	if _, err := VerifyNode(node.Certificate, node.Key, otherOS.Certificate, now); err == nil {
		t.Error("verified against a bundle without its OS CA")
	}
	if err := ValidateNodeCA(otherNodeCA, osCA.Certificate); err == nil {
		t.Error("another OS CA's node CA verified")
	}
	if _, err := VerifyNode(node.Certificate, node.Key, osCA.Certificate+node.Key, time.Time{}); err == nil {
		t.Error("verified against a bundle holding a key")
	}
}

func TestBundleLeavesOutRepeats(t *testing.T) {
	a, b, c := newTestCA(t), newTestCA(t), newTestCA(t)
	got := Bundle(a.Certificate, Bundle(b.Certificate, a.Certificate), c.Certificate+"\n\n", b.Certificate)
	if want := a.Certificate + b.Certificate + c.Certificate; got != want {
		t.Errorf("bundle of a, a and b, and c, b holds %d certificates, want a, b and c", strings.Count(got, "BEGIN"))
	}
	for name, bundle := range map[string]string{
		"after":   Bundle(a.Certificate, "garbage"),
		"before":  Bundle("garbage\n" + a.Certificate),
		"between": Bundle(a.Certificate+"garbage\n"+b.Certificate, c.Certificate),
	} {
		if _, err := ParseBundle(bundle); err == nil {
			t.Errorf("text %s the certificates: the bundle keeps no trace of it", name)
		}
	}
}

// TestValidateOSCABundle checks that a bundle of OS CAs holds self-signed roots alone.
func TestValidateOSCABundle(t *testing.T) {
	osCA, nodeCA := newTestNodeCA(t)
	other, _ := newTestNodeCA(t)
	if certs, err := ValidateOSCABundle(Bundle(osCA.Certificate, other.Certificate)); err != nil || len(certs) != 2 {
		t.Errorf("two roots: %v", err)
	}
	leaf, _ := IssueLeaf(osCA, Leaf{CommonName: "leaf", Client: true}, now)
	for name, bundle := range map[string]string{
		"a node CA": Bundle(osCA.Certificate, nodeCA.Certificate),
		"a leaf":    Bundle(osCA.Certificate, leaf.Certificate),
	} {
		if _, err := ValidateOSCABundle(bundle); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
