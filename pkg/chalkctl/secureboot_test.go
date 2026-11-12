package chalkctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/secureboot/secureboottest"
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
// reboot, as some firmware reports it, and the reboot comes later. Its TPM event log is eventLog,
// none for nil.
func enrollNode(t *testing.T, ta *testApp, secureBootAfter byte, early bool, eventLog []byte) *enrolledNode {
	t.Helper()
	n := &enrolledNode{written: map[string]secureboot.ParsedUpdate{}, rebooted: make(chan string, 1)}
	s := maintenanceNode()
	s.Paths.EFIVars = t.TempDir()
	s.Paths.EventLog = filepath.Join(t.TempDir(), "binary_bios_measurements")
	if eventLog != nil {
		writeFile(t, s.Paths.EventLog, string(eventLog))
	}
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
		n := enrollNode(t, ta, 1, false, nil)
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
	addr := enrollNode(t, ta, 0, false, nil).addr
	err := ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", addr, "--insecure", "--force", "--timeout", "300ms"})
	if err == nil || !strings.Contains(err.Error(), "the node booted again without Secure Boot on, it reports SECURE_BOOT_DISABLED") {
		t.Errorf("enroll on a node that does not turn Secure Boot on = %v", err)
	}

	ta = newTestApp(t)
	n := enrollNode(t, ta, 1, false, nil)
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
	n := enrollNode(t, ta, 1, true, nil)
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

// TestSecurebootEnrollOptionROMs adds the digests of the option ROMs the node lists with
// --option-roms, and those of --db-hash, to db once each, and prints the option ROMs.
func TestSecurebootEnrollOptionROMs(t *testing.T) {
	keys := filepath.Join(t.TempDir(), "keys")
	if err := secureboot.NewKeys(keys, "test"); err != nil {
		t.Fatal(err)
	}
	gpu, nic, other := sha256.Sum256([]byte("gpu")), sha256.Sum256([]byte("nic")), sha256.Sum256([]byte("other"))
	l := secureboottest.NewLog(2, secureboottest.AlgSHA256)
	l.Driver(secureboottest.EvBootServicesDriver, gpu, secureboottest.OptionROMPath(3))
	l.Driver(secureboottest.EvBootServicesDriver, nic, secureboottest.OptionROMPath(4))
	ta := newTestApp(t)
	n := enrollNode(t, ta, 1, false, l.Bytes)
	err := ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", n.addr, "--insecure", "--force",
		"--option-roms", "--db-hash", "sha256:" + hex.EncodeToString(other[:]), "--db-hash", strings.ToUpper(hex.EncodeToString(nic[:]))})
	if err != nil {
		t.Fatalf("%v\n%s", err, ta.stderr)
	}
	db, err := n.written["db"].DBEntries()
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]byte{other[:], nic[:], gpu[:]}; !slices.EqualFunc(db.SHA256, want, bytes.Equal) {
		t.Errorf("db's digests %x, want %x", db.SHA256, want)
	}
	if out, want := ta.stdout.String(), "option ROMs the firmware measured:\n  sha256:"+hex.EncodeToString(gpu[:])+" PciRoot(0x0)/Pci(0x3,0x0)/Offset(0x10a00,0x2f9ff)\n"; !strings.Contains(out, want) {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

// TestSecurebootEnrollRefusesOptionROMs prints the node's refusal of keys under which Secure Boot
// would refuse its option ROMs, and refuses a --db-hash that is no SHA-256 digest; without an
// event log, --option-roms says that the node lists none.
func TestSecurebootEnrollRefusesOptionROMs(t *testing.T) {
	keys := filepath.Join(t.TempDir(), "keys")
	if err := secureboot.NewKeys(keys, "test"); err != nil {
		t.Fatal(err)
	}
	gpu := sha256.Sum256([]byte("gpu"))
	l := secureboottest.NewLog(2, secureboottest.AlgSHA256)
	l.Driver(secureboottest.EvBootServicesDriver, gpu, secureboottest.OptionROMPath(3))
	ta := newTestApp(t)
	n := enrollNode(t, ta, 1, false, l.Bytes)
	err := ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", n.addr, "--insecure"})
	if err == nil || !strings.Contains(err.Error(), "sha256:"+hex.EncodeToString(gpu[:])) || !strings.Contains(err.Error(), "--option-roms") || len(n.written) != 0 {
		t.Errorf("enroll of keys that refuse an option ROM = %v, wrote %d", err, len(n.written))
	}

	for _, hash := range []string{"abc", strings.Repeat("g", 64)} {
		if err := ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", n.addr, "--insecure", "--db-hash", hash}); err == nil || !strings.Contains(err.Error(), "--db-hash") {
			t.Errorf("enroll --db-hash %s = %v", hash, err)
		}
	}

	ta = newTestApp(t)
	n = enrollNode(t, ta, 1, false, nil)
	if err := ta.run(context.Background(), []string{"secureboot", "enroll", "--keys", keys, "--address", n.addr, "--insecure", "--force", "--option-roms"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stderr.String(), "has no TPM") {
		t.Errorf("stderr %q, want the node's reason for listing no option ROMs", ta.stderr)
	}
}
