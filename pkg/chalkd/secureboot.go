package chalkd

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/sys/unix"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/arch"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// enrolment is a variable EnrollSecureBoot writes, in the order it writes them: PK last, as
// writing it ends Setup Mode.
type enrolment struct {
	name   string
	vendor [16]byte
	attrs  uint32
	update []byte
	parsed secureboot.ParsedUpdate
	// entries are the certificates the update adds and, for db, the digests of images.
	entries secureboot.Database
}

// EnrollSecureBoot writes the Secure Boot keys of the request into the firmware, which must be in
// Setup Mode: db and KEK appended, PK last. It checks the updates first: KEK and PK hold X.509
// certificates alone, PK one, db X.509 certificates and SHA-256 digests of images, and each is
// signed as firmware that verifies them in Setup Mode expects, db by a KEK certificate of the
// request, KEK and PK by the PK certificate. Unless forced, db as it will be must allow the
// drivers the TPM event log lists in PCR 2, and the boot loader and UKI the node booted must
// verify against it. A node that runs from its own disk deletes its STATE, which the TPM seals to
// PCR 7, so its next boot creates STATE again. The node answers and reboots once the three writes
// went through, with db and KEK as it reads them back, or with them empty when it cannot.
func (s *Server) EnrollSecureBoot(ctx context.Context, req *connect.Request[nodev1.EnrollSecureBootRequest]) (*connect.Response[nodev1.EnrollSecureBootResponse], error) {
	if !s.mu.TryLock() {
		return nil, layer.Errorf(connect.CodeAborted, "another change of the node is running")
	}
	defer s.mu.Unlock()
	if s.installed {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "the node is installed and reboots")
	}
	setup, err := s.readEFIVariable("SetupMode", secureboot.GlobalVendor)
	if err != nil {
		return nil, err
	}
	if len(setup) != 1 || setup[0] != 1 {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "the firmware is not in Setup Mode; clear the platform key in the firmware setup first")
	}
	vars := []*enrolment{
		{name: "db", vendor: secureboot.ImageSecurityDatabase, attrs: secureboot.AttrAppend, update: req.Msg.Db},
		{name: "KEK", vendor: secureboot.GlobalVariable, attrs: secureboot.AttrAppend, update: req.Msg.Kek},
		{name: "PK", vendor: secureboot.GlobalVariable, attrs: secureboot.AttrReplace, update: req.Msg.Pk},
	}
	if err := checkEnrolment(vars); err != nil {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "%w", err)
	}
	db, kek, err := s.readKeyDatabases()
	if err != nil {
		return nil, err
	}
	resp := &nodev1.EnrollSecureBootResponse{DbBefore: dbEntries(db), KekBefore: subjects(kek.Certificates)}
	if !req.Msg.Force {
		newDB := db
		newDB.Certificates = slices.Concat(db.Certificates, vars[0].entries.Certificates)
		newDB.SHA256 = slices.Concat(db.SHA256, vars[0].entries.SHA256)
		dbx, err := s.readEFIDatabase("dbx")
		if err != nil {
			return nil, err
		}
		drivers, unchecked, err := s.readDrivers()
		if err != nil {
			return nil, layer.Errorf(connect.CodeFailedPrecondition, "%w; pass --force to enrol the keys without checking the drivers it lists, which also skips the check of the boot loader and UKI", err)
		}
		if err := checkDrivers(drivers, newDB, dbx); err != nil {
			return nil, layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
		}
		resp.DriversUnchecked = unchecked
		if err := s.checkBootedUnder(newDB, dbx); err != nil {
			return nil, err
		}
	}
	// The TPM will not unseal STATE once the keys change PCR 7; the installer runs without one.
	if !s.Installer {
		if err := s.RemoveState(ctx); err != nil {
			return nil, fmt.Errorf("remove STATE, which the next boot creates again: %w", err)
		}
	}
	for _, v := range vars {
		log.Printf("enrolling Secure Boot keys: writing %s", v.name)
		if err := s.writeEFIVariable(v.name, secureboot.GUIDString(v.vendor), v.attrs, v.update); err != nil {
			return nil, fmt.Errorf("write %s: %w; the firmware stays in Setup Mode, as PK is written last, so enroll can run again", v.name, err)
		}
	}
	// The keys are written, so the node reboots whatever it reads back: the boot after the
	// enrolment is the one that enforces them.
	log.Print("enrolled Secure Boot keys; rebooting")
	s.rebootSoon()
	dbAfter, kekAfter, err := s.readKeyDatabases()
	if err != nil {
		log.Printf("read db and KEK after the enrolment: %v", err)
		dbAfter, kekAfter = secureboot.Database{}, secureboot.Database{}
	}
	resp.DbAfter, resp.KekAfter = dbEntries(dbAfter), subjects(kekAfter.Certificates)
	return connect.NewResponse(resp), nil
}

// SecureBootDrivers lists the drivers the TPM event log records in PCR 2, the option ROMs of
// graphics and network cards as a rule, or none and why when the node has no event log.
func (s *Server) SecureBootDrivers(ctx context.Context, req *connect.Request[nodev1.SecureBootDriversRequest]) (*connect.Response[nodev1.SecureBootDriversResponse], error) {
	drivers, unchecked, err := s.readDrivers()
	if err != nil {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
	}
	resp := &nodev1.SecureBootDriversResponse{NoEventLog: unchecked}
	for _, d := range drivers {
		resp.Drivers = append(resp.Drivers, &nodev1.SecureBootDriver{Sha256: d.SHA256, DevicePath: d.DevicePath})
	}
	return connect.NewResponse(resp), nil
}

// readDrivers reads the drivers measured into PCR 2 from the TPM event log. Without an event log,
// or with one it cannot read the drivers from, such as a TPM 1.2's, it returns none and why; it
// refuses a log that is corrupt.
func (s *Server) readDrivers() ([]secureboot.Driver, string, error) {
	data, err := os.ReadFile(s.Paths.EventLog)
	if errors.Is(err, fs.ErrNotExist) {
		if entries, err := os.ReadDir(s.Paths.TPM); err == nil && len(entries) > 0 {
			return nil, fmt.Sprintf("the node has a TPM, but no event log at %s, which would list the option ROMs the firmware loaded; the firmware may not have passed one to Linux", s.Paths.EventLog), nil
		}
		return nil, fmt.Sprintf("the node has no TPM, so no event log at %s lists the option ROMs the firmware loaded", s.Paths.EventLog), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read the TPM event log: %w", err)
	}
	drivers, err := secureboot.PCR2Drivers(data)
	if errors.Is(err, secureboot.ErrUnsupportedLog) {
		return nil, fmt.Sprintf("the node cannot read the option ROMs from its TPM event log %s: %v", s.Paths.EventLog, err), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read the TPM event log %s: %w", s.Paths.EventLog, err)
	}
	return drivers, "", nil
}

// checkDrivers checks that Secure Boot would load the drivers measured into PCR 2 under db and
// dbx. dbx must not list their digests. db must hold their digests, or a Microsoft CA that signs
// option ROMs: the UEFI CA of 2011, which signed most of them, or the Option ROM UEFI CA of 2023,
// which signs those Microsoft signs since. The event log does not say who signed a driver, so
// either CA counts as allowing all of them. Its error names the drivers it would refuse.
func checkDrivers(drivers []secureboot.Driver, db, dbx secureboot.Database) error {
	microsoft := slices.ContainsFunc(secureboot.MicrosoftOptionROMCAs(), func(ca *x509.Certificate) bool {
		return slices.ContainsFunc(db.Certificates, ca.Equal)
	})
	holds := func(digests [][]byte, d []byte) bool {
		return slices.ContainsFunc(digests, func(h []byte) bool { return bytes.Equal(h, d) })
	}
	var refused, revoked []string
	for _, d := range drivers {
		name := fmt.Sprintf("sha256:%x %s", d.SHA256, d.DevicePath)
		switch {
		case holds(dbx.SHA256, d.SHA256):
			revoked = append(revoked, name)
		case !microsoft && !holds(db.SHA256, d.SHA256):
			refused = append(refused, name)
		}
	}
	var problems []string
	if len(refused) > 0 {
		problems = append(problems, fmt.Sprintf("under the new db, Secure Boot would refuse the drivers the firmware loaded from outside its own firmware, as a rule the option ROMs of graphics and network cards: %s; "+
			"enrol their digests with --option-roms, or Microsoft's UEFI CAs with --microsoft", strings.Join(refused, ", ")))
	}
	if len(revoked) > 0 {
		problems = append(problems, fmt.Sprintf("dbx revokes drivers the firmware loaded from outside its own firmware, so Secure Boot would refuse them whatever db holds: %s; "+
			"replace the card's firmware with one dbx does not list", strings.Join(revoked, ", ")))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s; without them the machine may show nothing or boot from no network card or disk controller; pass --force to enrol the keys all the same, which also skips the check of the boot loader and UKI",
		strings.Join(problems, "; "))
}

// checkEnrolment parses the updates of db, KEK and PK, in that order, and checks their entries
// and signatures.
func checkEnrolment(vars []*enrolment) error {
	for _, v := range vars {
		var err error
		if v.parsed, err = secureboot.ParseUpdate(v.update); err != nil {
			return fmt.Errorf("the update of %s: %w", v.name, err)
		}
		if v.name == "db" {
			v.entries, err = v.parsed.DBEntries()
		} else {
			v.entries.Certificates, err = v.parsed.X509Certificates()
		}
		if err != nil {
			return fmt.Errorf("the update of %s: %w", v.name, err)
		}
		if len(v.entries.Certificates) == 0 {
			return fmt.Errorf("the update of %s holds no certificate", v.name)
		}
	}
	db, kek, pk := vars[0], vars[1], vars[2]
	if len(pk.entries.Certificates) != 1 {
		return fmt.Errorf("the update of PK holds %d certificates; PK is one", len(pk.entries.Certificates))
	}
	signers := map[*enrolment][]*x509.Certificate{db: kek.entries.Certificates, kek: pk.entries.Certificates, pk: pk.entries.Certificates}
	for _, v := range vars {
		var errs []error
		for _, c := range signers[v] {
			err := v.parsed.Verify(v.name, v.vendor, v.attrs, c)
			if err == nil {
				errs = nil
				break
			}
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			return fmt.Errorf("the update of %s is not signed for %s by the keys it enrols: %w", v.name, v.name, errors.Join(errs...))
		}
	}
	return nil
}

// checkBootedUnder checks that Secure Boot would start the boot loader and UKI the node booted
// under db and dbx.
func (s *Server) checkBootedUnder(db, dbx secureboot.Database) error {
	binaries, err := s.bootedBinaries()
	if err != nil {
		return layer.Errorf(connect.CodeFailedPrecondition, "find the boot loader and UKI this machine booted, which must verify against the new db: %w; pass --force to enrol the keys without the check", err)
	}
	for _, path := range binaries {
		if err := verifyFile(path, db, dbx); err != nil {
			return layer.Errorf(connect.CodeFailedPrecondition, "the installer or image this machine booted is not signed by a db certificate: %s: %w; sign it with the db key you enrol (chalkbuild sign), boot it, and run enroll again, or pass --force", filepath.Base(path), err)
		}
	}
	return nil
}

// bootedBinaries are the files of the boot loader and the UKI the node booted on the ESP.
func (s *Server) bootedBinaries() ([]string, error) {
	a, ok := arch.ByGOARCH(runtime.GOARCH)
	if !ok {
		return nil, fmt.Errorf("chalkos builds no images for %s", runtime.GOARCH)
	}
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		return nil, err
	}
	// The ESP is mounted when first read, which waits for its device until the mount times out;
	// without the device, it is not looked for.
	espDevice := filepath.Join(s.Paths.BootPartitions, "esp")
	if _, err := os.Stat(filepath.Join(s.Host.DevRoot, strings.TrimPrefix(espDevice, "/dev/"))); err != nil {
		return nil, fmt.Errorf("udev found no ESP on the disk the machine booted from, %s: %w", espDevice, err)
	}
	entries, err := esp.Entries(s.Paths.ESP)
	if err != nil {
		return nil, err
	}
	booted, err := upgrade.Booted(entries, s.Paths.EFIVars, running)
	if err != nil {
		return nil, err
	}
	return []string{filepath.Join(s.Paths.ESP, "EFI", "BOOT", a.BootLoader), filepath.Join(s.Paths.ESP, esp.LinuxDir, booted.File)}, nil
}

func verifyFile(path string, db, dbx secureboot.Database) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return secureboot.VerifySignature(f, info.Size(), db, dbx)
}

// readKeyDatabases reads db and KEK.
func (s *Server) readKeyDatabases() (db, kek secureboot.Database, err error) {
	if db, err = s.readEFIDatabase("db"); err != nil {
		return
	}
	kek, err = s.readEFIDatabase("KEK")
	return
}

// readEFIDatabase reads a signature database of the firmware, db, dbx or KEK; empty when it is
// not set.
func (s *Server) readEFIDatabase(name string) (secureboot.Database, error) {
	vendor := secureboot.SecurityVendor
	if name == "KEK" {
		vendor = secureboot.GlobalVendor
	}
	data, err := s.readEFIVariable(name, vendor)
	if err != nil {
		return secureboot.Database{}, err
	}
	db, err := secureboot.ParseDatabase(data)
	if err != nil {
		return secureboot.Database{}, fmt.Errorf("the firmware's %s: %w", name, err)
	}
	return db, nil
}

// readEFIVariable reads an EFI variable's value from efivarfs; nil when it is not set.
func (s *Server) readEFIVariable(name, vendor string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.Paths.EFIVars, name+"-"+vendor))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the EFI variable %s: %w", name, err)
	}
	// Four bytes of attributes precede the value.
	if len(data) < 4 {
		return nil, fmt.Errorf("the EFI variable %s is cut short", name)
	}
	return data[4:], nil
}

// writeEFIVariable writes an authenticated EFI variable through efivarfs, or through
// s.WriteEFIVariable.
func (s *Server) writeEFIVariable(name, vendor string, attrs uint32, payload []byte) error {
	path := filepath.Join(s.Paths.EFIVars, name+"-"+vendor)
	if s.WriteEFIVariable != nil {
		return s.WriteEFIVariable(path, attrs, payload)
	}
	return writeEFIVarFS(path, attrs, payload)
}

// writeEFIVarFS writes the attributes and the payload to the file of an EFI variable in efivarfs,
// in one write, as efivarfs takes them. efivarfs marks the files of variables such as db
// immutable, so an existing file's flag is cleared first.
func writeEFIVarFS(path string, attrs uint32, payload []byte) error {
	if err := clearImmutable(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(binary.LittleEndian.AppendUint32(nil, attrs), payload...))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// immutableFlag is FS_IMMUTABLE_FL of linux/fs.h, the flag of an immutable file.
const immutableFlag = 0x10

func clearImmutable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	flags, err := unix.IoctlGetUint32(int(f.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return fmt.Errorf("read the flags of %s: %w", path, err)
	}
	if flags&immutableFlag == 0 {
		return nil
	}
	if err := unix.IoctlSetPointerInt(int(f.Fd()), unix.FS_IOC_SETFLAGS, int(flags&^immutableFlag)); err != nil {
		return fmt.Errorf("clear the immutable flag of %s: %w", path, err)
	}
	return nil
}

// subjects are the certificates' subjects.
func subjects(certs []*x509.Certificate) []string {
	var s []string
	for _, c := range certs {
		s = append(s, c.Subject.String())
	}
	return s
}

// dbEntries are the subjects of db's certificates, then its SHA-256 digests of images, each as
// sha256:<hex>.
func dbEntries(db secureboot.Database) []string {
	s := subjects(db.Certificates)
	for _, h := range db.SHA256 {
		s = append(s, fmt.Sprintf("sha256:%x", h))
	}
	return s
}
