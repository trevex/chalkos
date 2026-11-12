package chalkd

import (
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
	certs  []*x509.Certificate
}

// EnrollSecureBoot writes the Secure Boot keys of the request into the firmware, which must be in
// Setup Mode: db and KEK appended, PK last. It checks the updates first: each holds X.509
// certificates alone, PK one, and is signed as firmware that verifies them in Setup Mode expects,
// db by a KEK certificate of the request, KEK and PK by the PK certificate. Unless forced, the boot
// loader and UKI the node booted must verify against db as it will be. A node that runs from its
// own disk deletes its STATE, which the TPM seals to PCR 7, so its next boot creates STATE again.
// The node answers and reboots once the three writes went through, with db and KEK as it reads
// them back, or with them empty when it cannot.
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
	if !req.Msg.Force {
		if err := s.checkBootedUnder(db, vars[0].certs); err != nil {
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
	return connect.NewResponse(&nodev1.EnrollSecureBootResponse{
		DbBefore: subjects(db.Certificates), DbAfter: subjects(dbAfter.Certificates),
		KekBefore: subjects(kek.Certificates), KekAfter: subjects(kekAfter.Certificates),
	}), nil
}

// checkEnrolment parses the updates of db, KEK and PK, in that order, and checks their
// certificates and signatures.
func checkEnrolment(vars []*enrolment) error {
	for _, v := range vars {
		var err error
		if v.parsed, err = secureboot.ParseUpdate(v.update); err != nil {
			return fmt.Errorf("the update of %s: %w", v.name, err)
		}
		if v.certs, err = v.parsed.X509Certificates(); err != nil {
			return fmt.Errorf("the update of %s: %w", v.name, err)
		}
		if len(v.certs) == 0 {
			return fmt.Errorf("the update of %s holds no certificate", v.name)
		}
	}
	db, kek, pk := vars[0], vars[1], vars[2]
	if len(pk.certs) != 1 {
		return fmt.Errorf("the update of PK holds %d certificates; PK is one", len(pk.certs))
	}
	signers := map[*enrolment][]*x509.Certificate{db: kek.certs, kek: pk.certs, pk: pk.certs}
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
// once the certificates are added to db.
func (s *Server) checkBootedUnder(db secureboot.Database, added []*x509.Certificate) error {
	dbx, err := s.readEFIDatabase("dbx")
	if err != nil {
		return err
	}
	db.Certificates = append(db.Certificates, added...)
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
