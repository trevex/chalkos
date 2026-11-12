package secureboot

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/secureboot/secureboottest"
)

// TestPCR2Drivers reads the boot and runtime service drivers of PCR 2 with their SHA-256 digests
// and device paths, and nothing of other PCRs or types.
func TestPCR2Drivers(t *testing.T) {
	rom, runtime, app, blob := sha256.Sum256([]byte("rom")), sha256.Sum256([]byte("runtime")), sha256.Sum256([]byte("app")), sha256.Sum256([]byte("blob"))
	for _, uintn := range []byte{1, 2} {
		l := secureboottest.NewLog(uintn, secureboottest.AlgSHA1, secureboottest.AlgSHA256)
		l.Event(0, 0x80000008, blob, []byte("platform firmware"))
		l.Driver(secureboottest.EvBootServicesDriver, rom, secureboottest.OptionROMPath(3))
		// A file path, \a in UTF-16, a node PCR2Drivers does not name.
		l.Driver(secureboottest.EvRuntimeServicesDriver, runtime, append(secureboottest.Node(4, 4, []byte("\\\x00a\x00\x00\x00")), secureboottest.End()...))
		l.Event(4, 0x80000003, app, l.ImageLoad(nil))
		l.Event(2, 0x80000006, blob, nil)
		drivers, err := PCR2Drivers(l.Bytes)
		if err != nil {
			t.Fatalf("uintnSize %d: %v", uintn, err)
		}
		want := []Driver{
			{SHA256: rom[:], DevicePath: "PciRoot(0x0)/Pci(0x3,0x0)/Offset(0x10a00,0x2f9ff)"},
			{SHA256: runtime[:], DevicePath: "a device path of 14 bytes"},
		}
		if !slices.EqualFunc(drivers, want, func(a, b Driver) bool {
			return bytes.Equal(a.SHA256, b.SHA256) && a.DevicePath == b.DevicePath
		}) {
			t.Errorf("uintnSize %d: PCR2Drivers = %+v, want %+v", uintn, drivers, want)
		}
	}
}

// TestPCR2DriversRefuses refuses logs it cannot read: cut short, or with lengths past their end,
// and, as unsupported, logs without the Spec ID event of crypto-agile logs or without a SHA-256
// bank.
func TestPCR2DriversRefuses(t *testing.T) {
	good := secureboottest.NewLog(2, secureboottest.AlgSHA256)
	good.Driver(secureboottest.EvBootServicesDriver, sha256.Sum256(nil), secureboottest.OptionROMPath(3))
	sha1Only := secureboottest.NewLog(2, secureboottest.AlgSHA1)
	sha1Only.Driver(secureboottest.EvBootServicesDriver, sha256.Sum256(nil), secureboottest.OptionROMPath(3))
	notSpecID := bytes.Clone(good.Bytes)
	copy(notSpecID[32:], "Spec ID Event02")
	// The Spec ID event gives the size of SHA-256 digests at byte 62.
	shortDigests := bytes.Clone(good.Bytes)
	binary.LittleEndian.PutUint16(shortDigests[62:], 20)
	hugeSpec := bytes.Clone(good.Bytes)
	binary.LittleEndian.PutUint32(hugeSpec[28:], 0xffffffff)
	hugePath := secureboottest.NewLog(2, secureboottest.AlgSHA256)
	hugePath.Event(2, secureboottest.EvBootServicesDriver, sha256.Sum256(nil), binary.LittleEndian.AppendUint64(make([]byte, 24), 1<<63+5))
	for _, tc := range []struct {
		name, want  string
		log         []byte
		unsupported bool
	}{
		{"empty", "cut short", nil, false},
		{"cut short", "cut short", good.Bytes[:len(good.Bytes)-3], false},
		{"a Spec ID event past the end", "cut short", hugeSpec, false},
		{"a device path past the end", "cut short", hugePath.Bytes, false},
		{"SHA-256 digests of 20 bytes", "20 bytes", shortDigests, false},
		{"not crypto-agile", "Spec ID Event03", notSpecID, true},
		{"no SHA-256", "SHA-256", sha1Only.Bytes, true},
	} {
		_, err := PCR2Drivers(tc.log)
		if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, ErrUnsupportedLog) != tc.unsupported {
			t.Errorf("%s: PCR2Drivers = %v, want an error naming %q, unsupported %v", tc.name, err, tc.want, tc.unsupported)
		}
	}
}

// FuzzPCR2Drivers reads any log without panicking or allocating by the lengths it holds.
func FuzzPCR2Drivers(f *testing.F) {
	for _, uintn := range []byte{1, 2} {
		l := secureboottest.NewLog(uintn, secureboottest.AlgSHA1, secureboottest.AlgSHA256)
		l.Driver(secureboottest.EvBootServicesDriver, sha256.Sum256([]byte("rom")), secureboottest.OptionROMPath(3))
		f.Add(l.Bytes)
	}
	f.Fuzz(func(t *testing.T, log []byte) {
		drivers, err := PCR2Drivers(log)
		if err == nil {
			for _, d := range drivers {
				if len(d.SHA256) != sha256.Size {
					t.Fatalf("a digest of %d bytes", len(d.SHA256))
				}
			}
		}
	})
}
