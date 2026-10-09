// Package ukitest builds small PE images with the sections of a UKI, for tests.
package ukitest

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
)

const (
	fileAlign    = 512
	sectionAlign = 4096
	optSize      = 240
)

func align(n, a int) int { return (n + a - 1) / a * a }

// Build returns a PE32+ EFI application holding the sections, in name order, with nothing to run.
// sbsign signs it, and debug/pe reads it, as they would a UKI.
func Build(sections map[string][]byte) []byte {
	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, name)
	}
	sort.Strings(names)
	headers := align(0x40+4+20+optSize+40*len(names), fileAlign)
	le := binary.LittleEndian
	out := make([]byte, headers)
	copy(out, "MZ")
	le.PutUint32(out[0x3c:], 0x40)
	copy(out[0x40:], "PE\x00\x00")
	coff := out[0x44:]
	le.PutUint16(coff[0:], 0x8664)
	le.PutUint16(coff[2:], uint16(len(names)))
	le.PutUint16(coff[16:], optSize)
	le.PutUint16(coff[18:], 0x22)
	opt := out[0x58:]
	le.PutUint16(opt[0:], 0x20b)
	le.PutUint64(opt[24:], 0x10000000)
	le.PutUint32(opt[32:], sectionAlign)
	le.PutUint32(opt[36:], fileAlign)
	le.PutUint32(opt[60:], uint32(headers))
	// EFI application.
	le.PutUint16(opt[68:], 10)
	le.PutUint32(opt[108:], 16)
	va := sectionAlign
	for i, name := range names {
		data := sections[name]
		raw := align(len(data), fileAlign)
		h := out[0x58+optSize+40*i:]
		copy(h[:8], name)
		le.PutUint32(h[8:], uint32(len(data)))
		le.PutUint32(h[12:], uint32(va))
		le.PutUint32(h[16:], uint32(raw))
		le.PutUint32(h[20:], uint32(len(out)))
		// Initialised, readable data.
		le.PutUint32(h[36:], 0x40000040)
		out = append(out, data...)
		out = append(out, make([]byte, raw-len(data))...)
		va += align(len(data), sectionAlign)
	}
	le.PutUint32(opt[56:], uint32(va))
	return out
}

// UKI builds a UKI-like image: os-release values and a command line.
func UKI(osRelease map[string]string, cmdline string) []byte {
	keys := make([]string, 0, len(osRelease))
	for k := range osRelease {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%q\n", k, osRelease[k])
	}
	return Build(map[string][]byte{".osrel": []byte(b.String()), ".cmdline": []byte(cmdline)})
}
