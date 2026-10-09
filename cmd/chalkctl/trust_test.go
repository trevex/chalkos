package main

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
)

// beginRotation starts rotating kind in the test cluster's secrets file.
func (ta *testApp) beginRotation(t *testing.T, kind string) {
	t.Helper()
	if err := ta.secrets.BeginRotation(kind, time.Now()); err != nil {
		t.Fatal(err)
	}
	data, err := ta.secrets.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ta.dir, "secrets.json"), string(data))
}

// TestClientFilesAndInstallsTrustBothOSCAs checks that while the OS CA rotates, client files
// verify nodes by both OS CAs and installed nodes trust both.
func TestClientFilesAndInstallsTrustBothOSCAs(t *testing.T) {
	ta := newTestApp(t)
	ta.beginRotation(t, pki.RotateOSCA)
	bundle := ta.secrets.OSCABundle()
	if certs, _ := pki.ParseBundle(bundle); len(certs) != 2 {
		t.Fatalf("the secrets file trusts %d OS CAs, want 2", len(certs))
	}

	out := filepath.Join(ta.dir, "alice.json")
	if err := ta.run(context.Background(), []string{"config", "new", "--name", "alice", "--role", "reader", "--out", out, "--manifest", filepath.Join(ta.dir, "manifest.json"), "--flake", ta.dir}); err != nil {
		t.Fatal(err)
	}
	c, err := client.ReadConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	if c.OSCA != bundle {
		t.Error("the client file does not hold both OS CAs")
	}

	s := maintenanceNode()
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request) error { got = req; return nil }
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--insecure"}, addr)); err != nil {
		t.Fatal(err)
	}
	if string(got.CA) != bundle {
		t.Error("the installed node does not get both OS CAs")
	}
	if _, err := pki.VerifyNode(string(got.NodeCertificate), string(got.NodeKey), bundle, time.Now()); err != nil {
		t.Error(err)
	}
}

func TestTrustLine(t *testing.T) {
	line := trustLine(&nodev1.TrustStatus{Name: "Kubernetes CA", Fingerprints: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}, Issuing: strings.Repeat("b", 64)})
	if line != "  Kubernetes CA\taaaaaaaaaaaaaaaa, bbbbbbbbbbbbbbbb (issues)" {
		t.Errorf("trust line %q", line)
	}
}

// TestInstallFromAnImageOfAnotherOSCA checks that installing from a maintenance image that
// trusts another OS CA than the secrets file, as one built before the OS CA rotated, fails
// closed and says so.
func TestInstallFromAnImageOfAnotherOSCA(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	s.InPlace = func(context.Context, install.Request) error { t.Error("installed"); return nil }
	other, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	self, _ := pki.SelfSigned("chalkd", time.Now())
	pair, _ := tls.X509KeyPair([]byte(self.Certificate), []byte(self.Key))
	pool, _ := pki.BundlePool(other.Certificate)
	s.ClientCAs = chalkd.StaticCAs(pool)
	addr := serveTLS(t, s.Handler(), chalkd.TLSConfig(chalkd.StaticCertificate(&pair), s.ClientCAs))
	for _, pin := range [][]string{{"--insecure"}, {"--fingerprint", pki.Fingerprint(pair.Certificate[0])}} {
		err := ta.run(context.Background(), ta.args(append([]string{"install", "n1"}, pin...), addr))
		if err == nil || !strings.Contains(err.Error(), "the maintenance image trusts another OS CA") {
			t.Errorf("%v: err = %v, want the image's OS CA named", pin, err)
		}
	}
}
