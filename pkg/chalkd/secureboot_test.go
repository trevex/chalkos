package chalkd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/secureboot/secureboottest"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// testEnrolKeys are new Secure Boot keys and their directory.
func testEnrolKeys(t *testing.T) (secureboot.Keys, string) {
	t.Helper()
	dir := t.TempDir()
	if err := secureboot.NewKeys(dir, "test"); err != nil {
		t.Fatal(err)
	}
	k, err := secureboot.LoadKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	return k, dir
}

// signedUpdate signs the certificates for the variable with the key pair.
func signedUpdate(t *testing.T, k secureboot.Keys, name string, attrs uint32, signer secureboot.KeyPair, certs ...*x509.Certificate) []byte {
	t.Helper()
	vendor := secureboot.GlobalVariable
	if name == "db" {
		vendor = secureboot.ImageSecurityDatabase
	}
	u, err := secureboot.SignUpdate(secureboot.Update{Name: name, Vendor: vendor, Attributes: attrs, Data: secureboot.X509SignatureLists(k.Owner, certs...)}, time.Now(), signer.Cert, signer.Key)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// enrolRequest is the request chalkctl secureboot enroll sends for the keys.
func enrolRequest(t *testing.T, k secureboot.Keys) *nodev1.EnrollSecureBootRequest {
	return &nodev1.EnrollSecureBootRequest{
		Db:  signedUpdate(t, k, "db", secureboot.AttrAppend, k.KEK, k.DB.Cert),
		Kek: signedUpdate(t, k, "KEK", secureboot.AttrAppend, k.PK, k.KEK.Cert),
		Pk:  signedUpdate(t, k, "PK", secureboot.AttrReplace, k.PK, k.PK.Cert),
	}
}

// firmware is what a test server writes to the firmware.
type firmware struct {
	writes       []string
	stateRemoved bool
	rebooted     chan struct{}
}

// enrolServer is a maintenance server whose firmware is in Setup Mode, or not, with a db that
// holds the certificate other. Its writes of EFI variables land in its efivars directory as
// firmware would store them: the value without the authentication descriptor, appended with
// APPEND_WRITE. Its boot disk holds an unsigned UKI and boot loader.
func enrolServer(t *testing.T, setupMode bool, other []byte) (*Server, *firmware) {
	t.Helper()
	s, _ := newTestServer(t, maintenance, vda)
	fw := &firmware{rebooted: make(chan struct{})}
	mode := "\x06\x00\x00\x00\x00"
	if setupMode {
		mode = "\x06\x00\x00\x00\x01"
	}
	write(t, filepath.Join(s.Paths.EFIVars, "SetupMode-"+globalVariable), mode)
	write(t, filepath.Join(s.Paths.EFIVars, "db-"+secureboot.SecurityVendor), "\x27\x00\x00\x00"+string(ukitest.SignatureList(other)))
	write(t, filepath.Join(s.Host.DevRoot, "disk", "chalk-boot", "esp"), "")
	writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", "chalkos_2.efi")
	uki, err := os.ReadFile(filepath.Join(s.Paths.ESP, "EFI", "Linux", "chalkos_2.efi"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.Paths.ESP, "EFI", "BOOT", "BOOTX64.EFI"), string(uki))
	s.WriteEFIVariable = func(path string, attrs uint32, payload []byte) error {
		fw.writes = append(fw.writes, fmt.Sprintf("%s %x", filepath.Base(path), attrs))
		p, err := secureboot.ParseUpdate(payload)
		if err != nil {
			return err
		}
		value := binary.LittleEndian.AppendUint32(nil, attrs&^0x40)
		if old, err := os.ReadFile(path); err == nil && attrs&0x40 != 0 {
			value = old
		}
		return os.WriteFile(path, append(value, p.Data...), 0o644)
	}
	s.RemoveState = func(context.Context) error {
		fw.stateRemoved = true
		return nil
	}
	s.RebootNode = func() { close(fw.rebooted) }
	return s, fw
}

// TestEnrollSecureBoot writes db, KEK and PK, PK last, with their attributes, answers the entries
// of db and KEK before and after, removes STATE, which the TPM seals to PCR 7, and
// reboots.
func TestEnrollSecureBoot(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	k, dir := testEnrolKeys(t)
	_, _, other := imagetest.Signer(t, t.TempDir(), "other")
	s, fw := enrolServer(t, true, other)
	// The image this machine booted is signed with the db key enrolled.
	for _, path := range []string{filepath.Join(s.Paths.ESP, "EFI", "Linux", "chalkos_2.efi"), filepath.Join(s.Paths.ESP, "EFI", "BOOT", "BOOTX64.EFI")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		write(t, path, string(imagetest.SBSign(t, data, filepath.Join(dir, "db.key"), filepath.Join(dir, "db.crt"))))
	}

	// db also gets the digest of an image, which the answer lists after the certificates.
	rom := sha256.Sum256([]byte("rom"))
	r := enrolRequest(t, k)
	r.Db = dbUpdate(t, k, rom)
	resp, err := s.EnrollSecureBoot(context.Background(), connect.NewRequest(r))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"db-" + secureboot.SecurityVendor + " 67", "KEK-" + globalVariable + " 67", "PK-" + globalVariable + " 27"}; !slices.Equal(fw.writes, want) {
		t.Errorf("writes %v, want %v", fw.writes, want)
	}
	got := resp.Msg
	if !slices.Equal(got.DbBefore, []string{"CN=other"}) || !slices.Equal(got.DbAfter, []string{"CN=other", "CN=test Secure Boot db", "sha256:" + hex.EncodeToString(rom[:])}) ||
		len(got.KekBefore) != 0 || !slices.Equal(got.KekAfter, []string{"CN=test Secure Boot KEK"}) {
		t.Errorf("answer %+v", got)
	}
	// The node has no event log, so it did not check the drivers of PCR 2.
	if !strings.Contains(got.DriversUnchecked, "has no TPM") {
		t.Errorf("DriversUnchecked = %q", got.DriversUnchecked)
	}
	if !fw.stateRemoved {
		t.Error("STATE was not removed")
	}
	select {
	case <-fw.rebooted:
	case <-time.After(time.Minute):
		t.Fatal("the node did not reboot")
	}
}

// TestEnrollSecureBootRefuses refuses firmware that is not in Setup Mode, updates it cannot read
// or that are not signed by the keys they enrol, and an image Secure Boot would refuse under the
// new db, unless forced; it writes nothing then.
func TestEnrollSecureBootRefuses(t *testing.T) {
	k, _ := testEnrolKeys(t)
	_, _, other := imagetest.Signer(t, t.TempDir(), "other")
	good := enrolRequest(t, k)
	for _, tc := range []struct {
		name      string
		setupMode bool
		change    func(r *nodev1.EnrollSecureBootRequest)
		code      connect.Code
		want      string
	}{
		{"not in Setup Mode", false, func(*nodev1.EnrollSecureBootRequest) {}, connect.CodeFailedPrecondition, "not in Setup Mode"},
		{"a malformed db update", true, func(r *nodev1.EnrollSecureBootRequest) { r.Db = r.Db[:20] }, connect.CodeInvalidArgument, "the update of db"},
		{"a KEK update of no signature lists", true, func(r *nodev1.EnrollSecureBootRequest) {
			u, err := secureboot.SignUpdate(secureboot.Update{Name: "KEK", Vendor: secureboot.GlobalVariable, Attributes: secureboot.AttrAppend, Data: []byte("garbage")}, time.Now(), k.PK.Cert, k.PK.Key)
			if err != nil {
				t.Fatal(err)
			}
			r.Kek = u
		}, connect.CodeInvalidArgument, "the update of KEK"},
		{"a KEK update of a digest", true, func(r *nodev1.EnrollSecureBootRequest) {
			data := append(secureboot.X509SignatureLists(k.Owner, k.KEK.Cert), secureboot.SHA256SignatureList(k.Owner, make([]byte, 32))...)
			u, err := secureboot.SignUpdate(secureboot.Update{Name: "KEK", Vendor: secureboot.GlobalVariable, Attributes: secureboot.AttrAppend, Data: data}, time.Now(), k.PK.Cert, k.PK.Key)
			if err != nil {
				t.Fatal(err)
			}
			r.Kek = u
		}, connect.CodeInvalidArgument, "the update of KEK"},
		{"two platform keys", true, func(r *nodev1.EnrollSecureBootRequest) {
			r.Pk = signedUpdate(t, k, "PK", secureboot.AttrReplace, k.PK, k.PK.Cert, k.KEK.Cert)
		}, connect.CodeInvalidArgument, "PK is one"},
		{"db signed by the PK", true, func(r *nodev1.EnrollSecureBootRequest) {
			r.Db = signedUpdate(t, k, "db", secureboot.AttrAppend, k.PK, k.DB.Cert)
		}, connect.CodeInvalidArgument, "the update of db is not signed for db"},
		{"a PK update signed for appending", true, func(r *nodev1.EnrollSecureBootRequest) {
			r.Pk = signedUpdate(t, k, "PK", secureboot.AttrAppend, k.PK, k.PK.Cert)
		}, connect.CodeInvalidArgument, "the update of PK is not signed"},
		{"an unsigned image", true, func(*nodev1.EnrollSecureBootRequest) {}, connect.CodeFailedPrecondition, "not signed by a db certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fw := enrolServer(t, tc.setupMode, other)
			r := &nodev1.EnrollSecureBootRequest{Db: good.Db, Kek: good.Kek, Pk: good.Pk}
			tc.change(r)
			_, err := s.EnrollSecureBoot(context.Background(), connect.NewRequest(r))
			if layer.CodeOf(err) != tc.code || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("EnrollSecureBoot = %v, want %v naming %q", err, tc.code, tc.want)
			}
			if len(fw.writes) > 0 || fw.stateRemoved {
				t.Errorf("wrote %v, removed STATE: %v", fw.writes, fw.stateRemoved)
			}
		})
	}

	// Forced, the unsigned image does not stop the keys; the installer has no STATE to remove.
	s, fw := enrolServer(t, true, other)
	s.Installer = true
	if _, err := s.EnrollSecureBoot(context.Background(), connect.NewRequest(&nodev1.EnrollSecureBootRequest{Db: good.Db, Kek: good.Kek, Pk: good.Pk, Force: true})); err != nil {
		t.Fatal(err)
	}
	if len(fw.writes) != 3 || fw.stateRemoved {
		t.Errorf("forced: wrote %v, removed STATE: %v", fw.writes, fw.stateRemoved)
	}
}

// TestEnrollSecureBootStopsBeforePK leaves PK unwritten when KEK cannot be written, and says the
// firmware stays in Setup Mode.
func TestEnrollSecureBootStopsBeforePK(t *testing.T) {
	k, _ := testEnrolKeys(t)
	_, _, other := imagetest.Signer(t, t.TempDir(), "other")
	s, fw := enrolServer(t, true, other)
	write := s.WriteEFIVariable
	s.WriteEFIVariable = func(path string, attrs uint32, payload []byte) error {
		if strings.HasPrefix(filepath.Base(path), "KEK-") {
			return errors.New("write error")
		}
		return write(path, attrs, payload)
	}
	r := enrolRequest(t, k)
	r.Force = true
	_, err := s.EnrollSecureBoot(context.Background(), connect.NewRequest(r))
	if err == nil || !strings.Contains(err.Error(), "write KEK: write error") || !strings.Contains(err.Error(), "stays in Setup Mode") {
		t.Errorf("EnrollSecureBoot = %v", err)
	}
	if !slices.Equal(fw.writes, []string{"db-" + secureboot.SecurityVendor + " 67"}) {
		t.Errorf("writes %v, want db alone", fw.writes)
	}
	select {
	case <-fw.rebooted:
		t.Error("the node rebooted")
	case <-time.After(noReboot):
	}
}

// TestEnrollSecureBootRebootsAfterTheWrites reboots once db, KEK and PK are written even when it
// cannot read db back, and answers with the lists before and none after.
func TestEnrollSecureBootRebootsAfterTheWrites(t *testing.T) {
	k, _ := testEnrolKeys(t)
	_, _, other := imagetest.Signer(t, t.TempDir(), "other")
	s, fw := enrolServer(t, true, other)
	write := s.WriteEFIVariable
	s.WriteEFIVariable = func(path string, attrs uint32, payload []byte) error {
		if strings.HasPrefix(filepath.Base(path), "db-") {
			return os.WriteFile(path, []byte("\x67\x00\x00\x00garbage"), 0o644)
		}
		return write(path, attrs, payload)
	}
	r := enrolRequest(t, k)
	r.Force = true
	resp, err := s.EnrollSecureBoot(context.Background(), connect.NewRequest(r))
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Msg; !slices.Equal(got.DbBefore, []string{"CN=other"}) || len(got.DbAfter) != 0 || len(got.KekAfter) != 0 {
		t.Errorf("answer %+v", got)
	}
	if want := []string{"KEK-" + globalVariable + " 67", "PK-" + globalVariable + " 27"}; !slices.Equal(fw.writes, want) {
		t.Errorf("writes %v, want %v after db", fw.writes, want)
	}
	select {
	case <-fw.rebooted:
	case <-time.After(time.Minute):
		t.Fatal("the node did not reboot")
	}
}

// TestEnrollSecureBootWithoutESP refuses at once when udev found no ESP on the boot disk, rather
// than waiting for the ESP's mount.
func TestEnrollSecureBootWithoutESP(t *testing.T) {
	k, _ := testEnrolKeys(t)
	_, _, other := imagetest.Signer(t, t.TempDir(), "other")
	s, fw := enrolServer(t, true, other)
	if err := os.Remove(filepath.Join(s.Host.DevRoot, "disk", "chalk-boot", "esp")); err != nil {
		t.Fatal(err)
	}
	_, err := s.EnrollSecureBoot(context.Background(), connect.NewRequest(enrolRequest(t, k)))
	if layer.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "udev found no ESP on the disk the machine booted from") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("EnrollSecureBoot = %v", err)
	}
	if len(fw.writes) > 0 {
		t.Errorf("wrote %v", fw.writes)
	}
}

// writeEventLog writes an event log of drivers measured into PCR 2, the option ROMs of the PCI
// devices 3, 4 and so on, of the digests, where the server reads it.
func writeEventLog(t *testing.T, s *Server, digests ...[32]byte) {
	t.Helper()
	l := secureboottest.NewLog(2, secureboottest.AlgSHA1, secureboottest.AlgSHA256)
	for i, d := range digests {
		l.Driver(secureboottest.EvBootServicesDriver, d, secureboottest.OptionROMPath(byte(3+i)))
	}
	write(t, s.Paths.EventLog, string(l.Bytes))
}

// dbUpdate is the update of db chalkctl sends for the keys with the digests of images.
func dbUpdate(t *testing.T, k secureboot.Keys, digests ...[32]byte) []byte {
	t.Helper()
	var hashes [][]byte
	for _, d := range digests {
		hashes = append(hashes, d[:])
	}
	data := append(secureboot.X509SignatureLists(k.Owner, k.DB.Cert), secureboot.SHA256SignatureList(k.Owner, hashes...)...)
	u, err := secureboot.SignUpdate(secureboot.Update{Name: "db", Vendor: secureboot.ImageSecurityDatabase, Attributes: secureboot.AttrAppend, Data: data}, time.Now(), k.KEK.Cert, k.KEK.Key)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestSecureBootDrivers lists the drivers of PCR 2 the event log holds, and none, with the
// reason, without an event log.
func TestSecureBootDrivers(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	resp, err := s.SecureBootDrivers(context.Background(), connect.NewRequest(&nodev1.SecureBootDriversRequest{}))
	if err != nil || len(resp.Msg.Drivers) != 0 || !strings.Contains(resp.Msg.NoEventLog, "has no TPM") {
		t.Errorf("SecureBootDrivers without a log = %+v, %v", resp, err)
	}

	// A TPM without an event log, and a log of SHA-1 digests alone, list nothing, and say why.
	write(t, filepath.Join(s.Paths.TPM, "tpm0", "dev"), "10:224\n")
	if resp, err := s.SecureBootDrivers(context.Background(), connect.NewRequest(&nodev1.SecureBootDriversRequest{})); err != nil || !strings.Contains(resp.Msg.NoEventLog, "has a TPM, but no event log") {
		t.Errorf("SecureBootDrivers with a TPM and no log = %+v, %v", resp, err)
	}
	sha1Only := secureboottest.NewLog(2, secureboottest.AlgSHA1)
	write(t, s.Paths.EventLog, string(sha1Only.Bytes))
	if resp, err := s.SecureBootDrivers(context.Background(), connect.NewRequest(&nodev1.SecureBootDriversRequest{})); err != nil || len(resp.Msg.Drivers) != 0 || !strings.Contains(resp.Msg.NoEventLog, "no SHA-256") {
		t.Errorf("SecureBootDrivers of a SHA-1 log = %+v, %v", resp, err)
	}

	rom, nic := sha256.Sum256([]byte("rom")), sha256.Sum256([]byte("nic"))
	writeEventLog(t, s, rom, nic)
	resp, err = s.SecureBootDrivers(context.Background(), connect.NewRequest(&nodev1.SecureBootDriversRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg
	if len(got.Drivers) != 2 || !bytes.Equal(got.Drivers[0].Sha256, rom[:]) || got.Drivers[0].DevicePath != "PciRoot(0x0)/Pci(0x3,0x0)/Offset(0x10a00,0x2f9ff)" ||
		!bytes.Equal(got.Drivers[1].Sha256, nic[:]) || got.NoEventLog != "" {
		t.Errorf("SecureBootDrivers = %+v", got)
	}

	// A log cut short is refused.
	write(t, s.Paths.EventLog, "garbage")
	if _, err := s.SecureBootDrivers(context.Background(), connect.NewRequest(&nodev1.SecureBootDriversRequest{})); err == nil || !strings.Contains(err.Error(), "event log") {
		t.Errorf("SecureBootDrivers of a garbled log = %v", err)
	}
}

// TestCheckDrivers allows the drivers of PCR 2 when db holds their digests or a Microsoft CA that
// signs option ROMs, unless dbx lists them, and otherwise names them and what allows them.
func TestCheckDrivers(t *testing.T) {
	k, _ := testEnrolKeys(t)
	rom, nic := sha256.Sum256([]byte("rom")), sha256.Sum256([]byte("nic"))
	drivers := []secureboot.Driver{{SHA256: rom[:], DevicePath: "PciRoot(0x0)/Pci(0x3,0x0)"}, {SHA256: nic[:], DevicePath: "PciRoot(0x0)/Pci(0x4,0x0)"}}
	ms := secureboot.MicrosoftDB()
	own := []*x509.Certificate{k.DB.Cert}
	for _, tc := range []struct {
		name    string
		drivers []secureboot.Driver
		db, dbx secureboot.Database
		refused []string
	}{
		{"no drivers", nil, secureboot.Database{Certificates: own}, secureboot.Database{}, nil},
		{"both digests", drivers, secureboot.Database{Certificates: own, SHA256: [][]byte{nic[:], rom[:]}}, secureboot.Database{}, nil},
		{"one digest", drivers, secureboot.Database{Certificates: own, SHA256: [][]byte{rom[:]}}, secureboot.Database{}, []string{"sha256:" + hex.EncodeToString(nic[:]) + " PciRoot(0x0)/Pci(0x4,0x0)", "--option-roms", "--microsoft"}},
		{"no digest", drivers, secureboot.Database{Certificates: own}, secureboot.Database{}, []string{"sha256:" + hex.EncodeToString(rom[:]), "sha256:" + hex.EncodeToString(nic[:])}},
		{"Microsoft UEFI CA 2011", drivers, secureboot.Database{Certificates: append(own, ms[0])}, secureboot.Database{}, nil},
		{"Microsoft Option ROM UEFI CA 2023", drivers, secureboot.Database{Certificates: []*x509.Certificate{ms[2]}}, secureboot.Database{}, nil},
		// The UEFI CA of 2023 signs boot loaders alone.
		{"Microsoft UEFI CA 2023", drivers, secureboot.Database{Certificates: []*x509.Certificate{ms[1]}}, secureboot.Database{}, []string{"sha256:" + hex.EncodeToString(rom[:])}},
		{"a Microsoft KEK CA", drivers, secureboot.Database{Certificates: secureboot.MicrosoftKEK()}, secureboot.Database{}, []string{"sha256:" + hex.EncodeToString(rom[:])}},
		{"a digest dbx lists", drivers, secureboot.Database{Certificates: append(own, ms[0]), SHA256: [][]byte{nic[:]}}, secureboot.Database{SHA256: [][]byte{nic[:]}},
			[]string{"dbx revokes", "sha256:" + hex.EncodeToString(nic[:]) + " PciRoot(0x0)/Pci(0x4,0x0)"}},
	} {
		err := checkDrivers(tc.drivers, tc.db, tc.dbx)
		if tc.refused == nil {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: allowed", tc.name)
			continue
		}
		for _, want := range append(tc.refused, "--force", "boot loader and UKI") {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %v, want it to name %q", tc.name, err, want)
			}
		}
		if tc.name == "a digest dbx lists" && strings.Contains(err.Error(), hex.EncodeToString(rom[:])) {
			t.Errorf("%s: %v names the option ROM a Microsoft CA allows", tc.name, err)
		}
	}
}

// TestEnrollSecureBootChecksDrivers refuses keys under which db would allow no driver of PCR 2 the
// event log lists, before it writes anything, and checks the image once db holds their digests;
// forced, it checks neither. A garbled event log is refused unless forced, and one of SHA-1
// digests alone is not checked.
func TestEnrollSecureBootChecksDrivers(t *testing.T) {
	k, _ := testEnrolKeys(t)
	_, _, other := imagetest.Signer(t, t.TempDir(), "other")
	rom := sha256.Sum256([]byte("rom"))
	for _, tc := range []struct {
		name  string
		log   string
		db    []byte
		force bool
		code  connect.Code
		want  string
	}{
		{"an option ROM db does not allow", "rom", nil, false, connect.CodeFailedPrecondition, "sha256:" + hex.EncodeToString(rom[:]) + " PciRoot(0x0)/Pci(0x3,0x0)"},
		{"the option ROM's digest", "rom", dbUpdate(t, k, rom), false, connect.CodeFailedPrecondition, "not signed by a db certificate"},
		{"a garbled event log", "garbage", nil, false, connect.CodeFailedPrecondition, "--force"},
		// A log of SHA-1 digests alone does not list the drivers, so the check of the image follows.
		{"a SHA-1 event log", "sha1", nil, false, connect.CodeFailedPrecondition, "not signed by a db certificate"},
		{"forced", "rom", nil, true, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fw := enrolServer(t, true, other)
			s.Installer = true
			switch tc.log {
			case "rom":
				writeEventLog(t, s, rom)
			case "sha1":
				write(t, s.Paths.EventLog, string(secureboottest.NewLog(2, secureboottest.AlgSHA1).Bytes))
			default:
				write(t, s.Paths.EventLog, "garbage")
			}
			r := enrolRequest(t, k)
			if tc.db != nil {
				r.Db = tc.db
			}
			r.Force = tc.force
			_, err := s.EnrollSecureBoot(context.Background(), connect.NewRequest(r))
			if tc.code == 0 {
				if err != nil || len(fw.writes) != 3 {
					t.Errorf("EnrollSecureBoot = %v, wrote %v", err, fw.writes)
				}
				return
			}
			if layer.CodeOf(err) != tc.code || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("EnrollSecureBoot = %v, want %v naming %q", err, tc.code, tc.want)
			}
			if len(fw.writes) > 0 {
				t.Errorf("wrote %v", fw.writes)
			}
		})
	}
}
