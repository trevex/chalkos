// Package secureboottest builds TPM event logs as firmware writes them, for tests.
package secureboottest

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
)

// TPM algorithm IDs of the digests of an event log, and the types of the events of drivers.
const (
	AlgSHA1                 = 0x0004
	AlgSHA256               = 0x000b
	EvBootServicesDriver    = 0x80000004
	EvRuntimeServicesDriver = 0x80000005
)

// Log is a TCG PC Client crypto-agile event log as firmware writes it: the Spec ID event in the
// SHA-1 format, naming the banks, then events in the crypto-agile format, each with a digest of
// every bank.
type Log struct {
	Bytes []byte
	banks []uint16
	uintn int
}

// NewLog starts a log of the banks whose UINTN fields are 4 bytes for uintnSize 1 and 8 bytes
// for 2.
func NewLog(uintnSize byte, banks ...uint16) *Log {
	sizes := map[uint16]uint16{AlgSHA1: sha1.Size, AlgSHA256: sha256.Size}
	spec := []byte("Spec ID Event03\x00")
	spec = binary.LittleEndian.AppendUint32(spec, 0)
	spec = append(spec, 0, 2, 0, uintnSize)
	spec = binary.LittleEndian.AppendUint32(spec, uint32(len(banks)))
	for _, alg := range banks {
		spec = binary.LittleEndian.AppendUint16(spec, alg)
		spec = binary.LittleEndian.AppendUint16(spec, sizes[alg])
	}
	spec = append(spec, 0)
	// EV_NO_ACTION in PCR 0.
	b := binary.LittleEndian.AppendUint32(nil, 0)
	b = binary.LittleEndian.AppendUint32(b, 3)
	b = append(b, make([]byte, sha1.Size)...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(spec)))
	return &Log{Bytes: append(b, spec...), banks: banks, uintn: 4 * int(uintnSize)}
}

// Event appends an event of the PCR and type whose SHA-256 digest is sha, its SHA-1 digest zero,
// with the data.
func (l *Log) Event(pcr, typ uint32, sha [32]byte, data []byte) {
	l.Bytes = binary.LittleEndian.AppendUint32(l.Bytes, pcr)
	l.Bytes = binary.LittleEndian.AppendUint32(l.Bytes, typ)
	l.Bytes = binary.LittleEndian.AppendUint32(l.Bytes, uint32(len(l.banks)))
	for _, alg := range l.banks {
		l.Bytes = binary.LittleEndian.AppendUint16(l.Bytes, alg)
		if alg == AlgSHA256 {
			l.Bytes = append(l.Bytes, sha[:]...)
		} else {
			l.Bytes = append(l.Bytes, make([]byte, sha1.Size)...)
		}
	}
	l.Bytes = binary.LittleEndian.AppendUint32(l.Bytes, uint32(len(data)))
	l.Bytes = append(l.Bytes, data...)
}

// Driver appends the event of a driver measured into PCR 2 from the device path.
func (l *Log) Driver(typ uint32, sha [32]byte, path []byte) {
	l.Event(2, typ, sha, l.ImageLoad(path))
}

// ImageLoad is a UEFI_IMAGE_LOAD_EVENT of the device path with the log's UINTN fields.
func (l *Log) ImageLoad(path []byte) []byte {
	b := binary.LittleEndian.AppendUint64(nil, 0x7e000000)
	put := func(v uint64) {
		if l.uintn == 4 {
			b = binary.LittleEndian.AppendUint32(b, uint32(v))
		} else {
			b = binary.LittleEndian.AppendUint64(b, v)
		}
	}
	put(0x20000)
	put(0)
	put(uint64(len(path)))
	return append(b, path...)
}

// Node is a device path node of the type and subtype.
func Node(typ, sub byte, data []byte) []byte {
	return append(binary.LittleEndian.AppendUint16([]byte{typ, sub}, uint16(4+len(data))), data...)
}

// End is the node that ends a device path.
func End() []byte { return Node(0x7f, 0xff, nil) }

// OptionROMPath is PciRoot(0x0)/Pci(dev,0x0)/Offset(0x10a00,0x2f9ff), the device path EDK2's PCI
// bus driver gives the image of the option ROM of the PCI device.
func OptionROMPath(dev byte) []byte {
	acpi := binary.LittleEndian.AppendUint32(nil, 0x0a0341d0)
	acpi = binary.LittleEndian.AppendUint32(acpi, 0)
	offset := binary.LittleEndian.AppendUint32(nil, 0)
	offset = binary.LittleEndian.AppendUint64(offset, 0x10a00)
	offset = binary.LittleEndian.AppendUint64(offset, 0x2f9ff)
	p := Node(2, 1, acpi)
	p = append(p, Node(1, 1, []byte{0, dev})...)
	p = append(p, Node(4, 8, offset)...)
	return append(p, End()...)
}
