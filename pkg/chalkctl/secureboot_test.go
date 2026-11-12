package chalkctl

import (
	"context"
	"crypto/x509"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/secureboot"
)

// TestSecurebootNew writes the keys named after their directory and prints how to sign images
// with them, and refuses a directory that holds files.
func TestSecurebootNew(t *testing.T) {
	ta := newTestApp(t)
	dir := filepath.Join(t.TempDir(), "lab-keys")
	if err := ta.run(context.Background(), []string{"secureboot", "new", dir}); err != nil {
		t.Fatal(err)
	}
	k, err := secureboot.LoadKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := k.DB.Cert.Subject.CommonName; got != "lab-keys Secure Boot db" {
		t.Errorf("db's common name %q", got)
	}
	if out := ta.stdout.String(); !strings.Contains(out, "chalkbuild sign --key "+filepath.Join(dir, "db.key")+" --cert "+filepath.Join(dir, "db.crt")) || !strings.Contains(out, "offline and backed up") {
		t.Errorf("stdout = %q", out)
	}
	if err := ta.run(context.Background(), []string{"secureboot", "new", dir, "--name", "other"}); err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Errorf("secureboot new over keys = %v", err)
	}
	// secureboot is a group, which runs nothing itself.
	if err := ta.run(context.Background(), []string{"secureboot", dir}); err == nil {
		t.Error("secureboot ran with an argument")
	}
}

// enrolledNode is a node enrollNode serves.
type enrolledNode struct {
	addr string
	// written are the updates written, by variable, as firmware stores them.
	written map[string]secureboot.ParsedUpdate
	// rebooted receives the fingerprint of the certificate the node presents once it rebooted.
	rebooted chan string
}

// enrollNode serves a node in maintenance mode whose firmware is in Setup Mode. Its reboot sets
// SecureBoot to secureBootAfter and has it present a new certificate, as chalkd does when it
// boots in maintenance mode. With early, SecureBoot is on as soon as PK is written, before the
// reboot, as some firmware reports it, and the reboot comes later.
func enrollNode(t *testing.T, ta *testApp, secureBootAfter byte, early bool) *enrolledNode {
	t.Helper()
	n := &enrolledNode{written: map[string]secureboot.ParsedUpdate{}, rebooted: make(chan string, 1)}
	s := maintenanceNode()
	s.Paths.EFIVars = t.TempDir()
	s.RebootDelay = 10 * time.Millisecond
	if early {
		s.RebootDelay = 300 * time.Millisecond
	}
	efiVar := func(name string, value byte) {
		writeFile(t, filepath.Join(s.Paths.EFIVars, name+"-"+secureboot.GlobalVendor), "\x06\x00\x00\x00"+string([]byte{value}))
	}
	efiVar("SetupMode", 1)
	s.WriteEFIVariable = func(path string, attrs uint32, payload []byte) error {
		p, err := secureboot.ParseUpdate(payload)
		if err != nil {
			return err
		}
		name := strings.Split(filepath.Base(path), "-")[0]
		n.written[name] = p
		if name == "PK" {
			efiVar("SetupMode", 0)
			if early {
				efiVar("SecureBoot", 1)
			}
		}
		return nil
	}
	s.RemoveState = func(context.Context) error { return nil }
	s.RebootNode = func() {
		efiVar("SecureBoot", secureBootAfter)
		n.rebooted <- ta.renewMaintenanceCertificate(t, n.addr)
	}
	ta.pollInterval = 10 * time.Millisecond
	n.addr = ta.startNode(t, s)
	return n
}

// TestSecurebootEnroll sends db signed by the KEK, KEK signed by the PK and PK signed by itself,
// Microsoft's certificates only with --microsoft, prints the node's answer and waits for Secure
// Boot to be on.
func TestSecurebootEnroll(t *testing.T) {
	keys := filepath.Join(t.TempDir(), "keys")
	if err := secureboot.NewKeys(keys, "test"); err != nil {
		t.Fatal(err)
	}
	k, err := secureboot.LoadKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, microsoft := range []bool{false, true} {
		ta := newTestApp(t)
		n := enrollNode(t, ta, 1, false)
		addr, written := n.addr, n.written
		args := []string{"secureboot", "enroll", "--keys", keys, "--address", addr, "--insecure", "--force"}
		if microsoft {
			args = append(args, "--microsoft")
		}
		if err := ta.run(context.Background(), args); err != nil {
			t.Fatalf("microsoft %v: %v\n%s", microsoft, err, ta.stderr)
		}
		wantDB, wantKEK := []*x509.Certificate{k.DB.Cert}, []*x509.Certificate{k.KEK.Cert}
		if microsoft {
			wantDB, wantKEK = append(wantDB, secureboot.MicrosoftDB()...), append(wantKEK, secureboot.MicrosoftKEK()...)
		}
		for _, tc := range []struct {
			name   string
			vendor [16]byte
			attrs  uint32
			signer *x509.Certificate
			want   []*x509.Certificate
		}{
			{"db", secureboot.ImageSecurityDatabase, secureboot.AttrAppend, k.KEK.Cert, wantDB},
			{"KEK", secureboot.GlobalVariable, secureboot.AttrAppend, k.PK.Cert, wantKEK},
			{"PK", secureboot.GlobalVariable, secureboot.AttrReplace, k.PK.Cert, []*x509.Certificate{k.PK.Cert}},
		} {
			p, ok := written[tc.name]
			if !ok {
				t.Fatalf("microsoft %v: %s was not written", microsoft, tc.name)
			}
			if err := p.Verify(tc.name, tc.vendor, tc.attrs, tc.signer); err != nil {
				t.Errorf("microsoft %v: %s: %v", microsoft, tc.name, err)
			}
			certs, err := p.X509Certificates()
			if err != nil || !slices.EqualFunc(certs, tc.want, (*x509.Certificate).Equal) {
				t.Errorf("microsoft %v: %s holds %d certificates, %v; want %d", microsoft, tc.name, len(certs), err, len(tc.want))
			}
		}
		out := ta.stdout.String()
		for _, want := range []string{"db before: empty\n", "KEK before: empty\n", "the node reboots with Secure Boot enforced\n", "Secure Boot is on; install the node with chalkctl install\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("microsoft %v: stdout %q, want %q", microsoft, out, want)
			}
		}
	}
}

// TestSecurebootEnrollRefusals prints the node's refusal of firmware that is not in Setup Mode,
// and stops waiting for a node that does not boot with Secure Boot on.
func TestSecurebootEnrollRefusals(t *testing.T) {
	keys := filepath.Join(t.TempDir(), "keys")
	if err := secureboot.NewKeys(keys, "test"); err != nil {
		t.Fatal(err)
	}

	ta := newTestApp(t)
	addr := enrollNode(t, ta, 0, false).addr
	err := ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", addr, "--insecure", "--force", "--timeout", "300ms"})
	if err == nil || !strings.Contains(err.Error(), "the node booted again without Secure Boot on, it reports SECURE_BOOT_DISABLED") {
		t.Errorf("enroll on a node that does not turn Secure Boot on = %v", err)
	}

	ta = newTestApp(t)
	n := enrollNode(t, ta, 1, false)
	addr, written := n.addr, n.written
	ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", addr, "--insecure", "--force"})
	ta.stdout.Reset()
	err = ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", addr, "--insecure", "--force"})
	if err == nil || !strings.Contains(err.Error(), "not in Setup Mode") || len(written) != 3 {
		t.Errorf("enroll out of Setup Mode = %v, wrote %d", err, len(written))
	}

	ta = newTestApp(t)
	if err := os.Remove(filepath.Join(keys, "owner.txt")); err != nil {
		t.Fatal(err)
	}
	if err := ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", addr, "--insecure"}); err == nil || !strings.Contains(err.Error(), "owner.txt") {
		t.Errorf("enroll without owner.txt = %v", err)
	}
}

// TestSecurebootEnrollWaitsForTheReboot keeps waiting while the boot that wrote the keys reports
// Secure Boot on, as some firmware does once PK is written, until the node booted again with its
// new certificate.
func TestSecurebootEnrollWaitsForTheReboot(t *testing.T) {
	keys := filepath.Join(t.TempDir(), "keys")
	if err := secureboot.NewKeys(keys, "test"); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp(t)
	n := enrollNode(t, ta, 1, true)
	if err := ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", n.addr, "--insecure", "--force"}); err != nil {
		t.Fatal(err)
	}
	var rebooted string
	select {
	case rebooted = <-n.rebooted:
	default:
		t.Fatal("enroll returned before the node rebooted")
	}
	if !strings.Contains(ta.stderr.String(), "fingerprint is now "+rebooted) {
		t.Errorf("stderr = %q, want the rebooted node's fingerprint %s", ta.stderr, rebooted)
	}
}
