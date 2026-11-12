package secureboot

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// EventLog is where Linux exposes the event log the firmware wrote of its measurements into the
// TPM.
const EventLog = "/sys/kernel/security/tpm0/binary_bios_measurements"

// Event types and TPM algorithm IDs of the TCG PC Client Platform Firmware Profile's event log.
const (
	evNoAction              = 0x3
	evBootServicesDriver    = 0x80000004
	evRuntimeServicesDriver = 0x80000005
	algSHA1                 = 0x0004
	algSHA256               = 0x000b
)

// Driver is a UEFI driver the firmware loaded from outside its own firmware volumes and measured
// into PCR 2, as a rule the firmware of a graphics or network card, its option ROM.
type Driver struct {
	// SHA256 is the image's Authenticode digest, which db holds to allow the image.
	SHA256 []byte
	// DevicePath is where the firmware found the image, as UEFI writes device paths, or its size
	// when it holds nodes DevicePath does not name.
	DevicePath string
}

// ErrUnsupportedLog is the error of PCR2Drivers for an event log it cannot read the drivers from
// although it is intact: a log of a TPM 1.2, or one without SHA-256 digests.
var ErrUnsupportedLog = errors.New("the event log holds no SHA-256 digests")

// PCR2Drivers reads the drivers measured into PCR 2 from an event log in the crypto-agile format
// of the TCG PC Client Platform Firmware Profile: the Spec ID event in the format of TPM 1.2
// logs, which names the digests each event holds, then events of the TCG_PCR_EVENT2 format. The
// log must hold SHA-256 digests; a log that does not, or is not crypto-agile, is refused with an
// error that wraps ErrUnsupportedLog.
func PCR2Drivers(log []byte) ([]Driver, error) {
	r := logReader{b: log}
	// TCG_PCClientPCREvent: the PCR, the type, a SHA-1 digest and the data.
	r.uint32()
	typ := r.uint32()
	r.bytes(20)
	spec := logReader{b: r.bytes(uint64(r.uint32()))}
	if r.err != nil {
		return nil, fmt.Errorf("the event log: %w", r.err)
	}
	// TCG_EfiSpecIDEventStruct: the signature, the platform class, the version and errata, the
	// size of UINTN in units of 4 bytes, then the size of the digest of each algorithm.
	if typ != evNoAction || !bytes.Equal(spec.bytes(16), []byte("Spec ID Event03\x00")) {
		return nil, fmt.Errorf("%w: it does not start with the event Spec ID Event03 of crypto-agile logs", ErrUnsupportedLog)
	}
	spec.bytes(7)
	uintn := 4 * int(spec.bytes(1)[0])
	sizes := map[uint16]uint64{}
	for n := spec.uint32(); n > 0 && spec.err == nil; n-- {
		alg := spec.uint16()
		sizes[alg] = uint64(spec.uint16())
	}
	if spec.err != nil {
		return nil, fmt.Errorf("the event log's Spec ID event: %w", spec.err)
	}
	switch sizes[algSHA256] {
	case 0:
		return nil, fmt.Errorf("%w: its Spec ID event names no SHA-256 bank", ErrUnsupportedLog)
	case sha256.Size:
	default:
		return nil, fmt.Errorf("the event log's Spec ID event gives SHA-256 digests %d bytes", sizes[algSHA256])
	}
	if uintn != 4 && uintn != 8 {
		return nil, fmt.Errorf("the event log's Spec ID event gives UINTN %d bytes", uintn)
	}
	var drivers []Driver
	for len(r.b) > 0 {
		pcr, typ := r.uint32(), r.uint32()
		var digest []byte
		for n := r.uint32(); n > 0 && r.err == nil; n-- {
			alg := r.uint16()
			size, ok := sizes[alg]
			if !ok {
				return nil, fmt.Errorf("an event of the event log holds a digest of the algorithm %#x, which the Spec ID event does not name", alg)
			}
			if d := r.bytes(size); alg == algSHA256 {
				digest = bytes.Clone(d)
			}
		}
		data := r.bytes(uint64(r.uint32()))
		if r.err != nil {
			return nil, fmt.Errorf("the event log: %w", r.err)
		}
		if pcr != 2 || typ != evBootServicesDriver && typ != evRuntimeServicesDriver {
			continue
		}
		if digest == nil {
			return nil, errors.New("a driver's event in the event log holds no SHA-256 digest")
		}
		// UEFI_IMAGE_LOAD_EVENT: the image's address, its length, its link-time address and the
		// device path's length, then the device path.
		load := logReader{b: data}
		load.bytes(uint64(8 + 2*uintn))
		path := load.bytes(load.uint(uintn))
		if load.err != nil {
			return nil, fmt.Errorf("a driver's event in the event log: %w", load.err)
		}
		drivers = append(drivers, Driver{SHA256: digest, DevicePath: devicePathString(path)})
	}
	return drivers, nil
}

// logReader reads little-endian fields; once the data runs out, it keeps err and returns zeros.
type logReader struct {
	b   []byte
	err error
}

// bytes returns the next n bytes. When fewer remain, it sets err and returns at most 8 zero
// bytes, enough for the integers read from it, as n comes from the log and may be huge.
func (r *logReader) bytes(n uint64) []byte {
	if r.err != nil || n > uint64(len(r.b)) {
		r.err = errors.New("cut short")
		r.b = nil
		return make([]byte, min(n, 8))
	}
	b := r.b[:n]
	r.b = r.b[n:]
	return b
}

func (r *logReader) uint16() uint16 { return binary.LittleEndian.Uint16(r.bytes(2)) }
func (r *logReader) uint32() uint32 { return binary.LittleEndian.Uint32(r.bytes(4)) }

// uint reads an unsigned integer of 4 or 8 bytes.
func (r *logReader) uint(size int) uint64 {
	if size == 4 {
		return uint64(r.uint32())
	}
	return binary.LittleEndian.Uint64(r.bytes(8))
}

// devicePathString writes a device path of the nodes that lead to an option ROM as UEFI writes
// them, such as PciRoot(0x0)/Pci(0x3,0x0)/Offset(0x10a00,0x2f9ff); a path of other nodes as its
// size.
func devicePathString(path []byte) string {
	other := fmt.Sprintf("a device path of %d bytes", len(path))
	var nodes []string
	for rest := path; len(rest) > 0; {
		if len(rest) < 4 {
			return other
		}
		typ, sub, size := rest[0], rest[1], int(binary.LittleEndian.Uint16(rest[2:]))
		if size < 4 || size > len(rest) {
			return other
		}
		data := rest[4:size]
		rest = rest[size:]
		switch {
		case typ == 0x7f && sub == 0xff:
			// The end of the path.
			return strings.Join(nodes, "/")
		case typ == 2 && sub == 1 && len(data) == 8:
			// An ACPI node: a PCI root bridge of the EISA ID PNP0A03, or PNP0A08 of PCI Express.
			uid := binary.LittleEndian.Uint32(data[4:])
			switch binary.LittleEndian.Uint32(data) {
			case 0x0a0341d0:
				nodes = append(nodes, fmt.Sprintf("PciRoot(%#x)", uid))
			case 0x0a0841d0:
				nodes = append(nodes, fmt.Sprintf("PcieRoot(%#x)", uid))
			default:
				return other
			}
		case typ == 1 && sub == 1 && len(data) == 2:
			// A PCI node: the function, then the device.
			nodes = append(nodes, fmt.Sprintf("Pci(%#x,%#x)", data[1], data[0]))
		case typ == 4 && sub == 8 && len(data) == 20:
			// A relative offset range, of the image in the ROM: a reserved field, then the first
			// and the last byte.
			nodes = append(nodes, fmt.Sprintf("Offset(%#x,%#x)", binary.LittleEndian.Uint64(data[4:]), binary.LittleEndian.Uint64(data[12:])))
		default:
			return other
		}
	}
	return other
}
