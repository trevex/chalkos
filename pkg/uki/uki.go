// Package uki reads what a unified kernel image says about the image it boots, from its
// os-release and command line sections, and checks its Secure Boot signature as firmware does.
package uki

import (
	"bufio"
	"debug/pe"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Image is what a UKI's sections say: the os-release of the image and the kernel command line.
type Image struct {
	OSRelease map[string]string
	Cmdline   string
}

// Read reads a UKI's .osrel and .cmdline sections.
func Read(r io.ReaderAt) (Image, error) {
	f, err := pe.NewFile(r)
	if err != nil {
		return Image{}, fmt.Errorf("not a UKI: %w", err)
	}
	defer f.Close()
	osrel, err := section(f, ".osrel")
	if err != nil {
		return Image{}, err
	}
	cmdline, err := section(f, ".cmdline")
	if err != nil {
		return Image{}, err
	}
	return Image{OSRelease: ParseOSRelease(osrel), Cmdline: strings.TrimSpace(cmdline)}, nil
}

// maxSection bounds what Read takes from a section; os-release and command lines are small.
const maxSection = 1 << 20

// section returns a section's content as systemd-stub reads it: its virtual size of the raw
// data, without the zeros that pad it.
func section(f *pe.File, name string) (string, error) {
	s := f.Section(name)
	if s == nil {
		return "", fmt.Errorf("the UKI has no %s section", name)
	}
	size := min(s.VirtualSize, s.Size)
	if size > maxSection {
		return "", fmt.Errorf("the UKI's %s section has %d bytes", name, size)
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(s.Open(), b); err != nil {
		return "", fmt.Errorf("read the UKI's %s section: %w", name, err)
	}
	return strings.TrimRight(string(b), "\x00"), nil
}

// ParseOSRelease reads os-release lines of KEY=value, with the value quoted or not.
func ParseOSRelease(text string) map[string]string {
	values := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			values[k] = unquote(v)
		}
	}
	return values
}

func unquote(v string) string {
	if len(v) < 2 || v[0] != '"' && v[0] != '\'' || v[len(v)-1] != v[0] {
		return v
	}
	v = v[1 : len(v)-1]
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// ID, Version, Cluster, Role and BootTries are the image's os-release values chalkos sets.
func (i Image) ID() string        { return i.OSRelease["IMAGE_ID"] }
func (i Image) Version() string   { return i.OSRelease["IMAGE_VERSION"] }
func (i Image) Cluster() string   { return i.OSRelease["CHALKOS_CLUSTER"] }
func (i Image) Role() string      { return i.OSRelease["CHALKOS_ROLE"] }
func (i Image) BootTries() string { return i.OSRelease["CHALKOS_BOOT_TRIES"] }

// UsrHash is the verity root hash of the store the UKI boots, from usrhash= on its command line.
func (i Image) UsrHash() ([]byte, error) {
	return UsrHash(i.Cmdline)
}

// UsrHash reads usrhash= from a kernel command line: the verity root hash of the store, whose
// partitions systemd finds by the UUIDs it derives from the hash.
func UsrHash(cmdline string) ([]byte, error) {
	var found []string
	for _, arg := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(arg, "usrhash="); ok {
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("the command line has %d usrhash= arguments, want one", len(found))
	}
	hash, err := hex.DecodeString(found[0])
	if err != nil || len(hash) != 32 {
		return nil, errors.New("usrhash= is not a SHA-256 root hash")
	}
	return hash, nil
}
