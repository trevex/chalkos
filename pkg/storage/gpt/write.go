package gpt

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"unicode/utf16"
)

// sectorSize is the sector size of the disk images WriteTable writes.
const sectorSize = 512

// The GPT WriteTable writes: 128 entries of 128 bytes, which take 32 sectors after each header,
// and partitions from 1 MiB on, as systemd-repart lays out an image.
const (
	entryCount     = 128
	entrySize      = 128
	entrySectors   = entryCount * entrySize / sectorSize
	firstUsableLBA = 2048
)

// AttrReadOnly is the GPT attribute that marks a partition read-only, as systemd-repart marks an
// image's store partitions.
const AttrReadOnly = uint64(1) << 60

// Entry is a partition WriteTable writes. Start and Size are in bytes, multiples of 512; Type
// and UUID are UUIDs; Name is the partition's label.
type Entry struct {
	Start, Size      int64
	Type, UUID, Name string
	Attributes       uint64
}

// TableSize is the size of a disk image whose last partition ends at end: the backup GPT's
// entries and header follow it.
func TableSize(end int64) int64 { return end + (entrySectors+1)*sectorSize }

// WriteTable writes a protective MBR and a GPT of 512-byte sectors with the disk GUID and the
// entries, the primary table at the start of a disk image of size bytes and the backup at its end.
// The entries must lie between 1 MiB and the backup table.
func WriteTable(w io.WriterAt, size int64, diskGUID string, entries []Entry) error {
	if size%sectorSize != 0 || len(entries) > entryCount {
		return fmt.Errorf("a GPT of %d entries on a disk of %d bytes", len(entries), size)
	}
	lastLBA := size/sectorSize - 1
	lastUsable := lastLBA - entrySectors - 1
	array := make([]byte, entryCount*entrySize)
	for i, e := range entries {
		switch {
		case e.Start%sectorSize != 0 || e.Size%sectorSize != 0 || e.Size <= 0:
			return fmt.Errorf("partition %d (%s) is not a whole number of sectors", i+1, e.Name)
		case e.Start/sectorSize < firstUsableLBA || (e.Start+e.Size)/sectorSize-1 > lastUsable:
			return fmt.Errorf("partition %d (%s) lies outside the disk's usable sectors", i+1, e.Name)
		}
		b := array[i*entrySize : (i+1)*entrySize]
		if err := putGUID(b[0:16], e.Type); err != nil {
			return err
		}
		if err := putGUID(b[16:32], e.UUID); err != nil {
			return err
		}
		binary.LittleEndian.PutUint64(b[32:], uint64(e.Start/sectorSize))
		binary.LittleEndian.PutUint64(b[40:], uint64((e.Start+e.Size)/sectorSize-1))
		binary.LittleEndian.PutUint64(b[48:], e.Attributes)
		name := utf16.Encode([]rune(e.Name))
		if len(name) > 36 {
			return fmt.Errorf("the partition name %q is longer than 36 UTF-16 units", e.Name)
		}
		for j, u := range name {
			binary.LittleEndian.PutUint16(b[56+2*j:], u)
		}
	}
	var disk [16]byte
	if err := putGUID(disk[:], diskGUID); err != nil {
		return err
	}
	header := func(my, alternate, entriesLBA int64) []byte {
		h := make([]byte, sectorSize)
		copy(h, "EFI PART")
		binary.LittleEndian.PutUint32(h[8:], 0x00010000)
		binary.LittleEndian.PutUint32(h[12:], 92)
		binary.LittleEndian.PutUint64(h[24:], uint64(my))
		binary.LittleEndian.PutUint64(h[32:], uint64(alternate))
		binary.LittleEndian.PutUint64(h[40:], firstUsableLBA)
		binary.LittleEndian.PutUint64(h[48:], uint64(lastUsable))
		copy(h[56:], disk[:])
		binary.LittleEndian.PutUint64(h[72:], uint64(entriesLBA))
		binary.LittleEndian.PutUint32(h[80:], entryCount)
		binary.LittleEndian.PutUint32(h[84:], entrySize)
		binary.LittleEndian.PutUint32(h[88:], crc32.ChecksumIEEE(array))
		binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h[:92]))
		return h
	}
	mbr := make([]byte, sectorSize)
	// One partition of the type 0xEE covers the disk, as far as 32-bit sector numbers reach.
	copy(mbr[446:], []byte{0x00, 0x00, 0x02, 0x00, 0xee, 0xff, 0xff, 0xff})
	binary.LittleEndian.PutUint32(mbr[454:], 1)
	binary.LittleEndian.PutUint32(mbr[458:], uint32(min(lastLBA, 0xffffffff)))
	mbr[510], mbr[511] = 0x55, 0xaa
	for _, s := range []struct {
		lba  int64
		data []byte
	}{
		{0, mbr},
		{1, header(1, lastLBA, 2)},
		{2, array},
		{lastLBA - entrySectors, array},
		{lastLBA, header(lastLBA, 1, lastLBA-entrySectors)},
	} {
		if _, err := w.WriteAt(s.data, s.lba*sectorSize); err != nil {
			return err
		}
	}
	return nil
}

// putGUID writes a UUID in GPT's byte order: its first three fields little-endian.
func putGUID(b []byte, uuid string) error {
	raw, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	if err != nil || len(raw) != 16 {
		return fmt.Errorf("%q is no UUID", uuid)
	}
	b[0], b[1], b[2], b[3] = raw[3], raw[2], raw[1], raw[0]
	b[4], b[5] = raw[5], raw[4]
	b[6], b[7] = raw[7], raw[6]
	copy(b[8:16], raw[8:])
	return nil
}
