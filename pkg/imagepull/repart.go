package imagepull

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"github.com/trevex/chalkos/pkg/storage/gpt"
)

// repartSeed is the seed an image's build runs systemd-repart with: the default of NixOS's
// image.repart.seed, which chalkos keeps. repart derives the disk's GUID, the ESP's partition UUID
// and its file system's volume ID from it, so every build has the same ones, and a pull gives a
// raw disk image the same ones as its build.
const repartSeed = "0867da16-f251-457d-a9e8-c31f9a3c220b"

// deriveUUID is a UUID derived from base and token as systemd-repart derives one: the first half
// of the HMAC-SHA256 of token keyed by base's bytes, marked as a version 4 UUID.
func deriveUUID(base string, token []byte) string {
	key, _ := hex.DecodeString(strings.ReplaceAll(base, "-", ""))
	m := hmac.New(sha256.New, key)
	m.Write(token)
	id := m.Sum(nil)[:16]
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return gpt.FormatUUID(id)
}

// diskUUID is the disk GUID systemd-repart gives an image's raw disk image.
func diskUUID() string { return deriveUUID(repartSeed, []byte("disk-uuid")) }

// espUUID is the partition UUID systemd-repart gives the ESP, the first partition of its type: it
// is keyed by the seed and derived from the type's bytes alone.
func espUUID() string {
	typ, _ := hex.DecodeString(strings.ReplaceAll(gpt.TypeESP, "-", ""))
	return deriveUUID(repartSeed, typ)
}

// espVolumeID is the volume ID systemd-repart has mkfs.fat give the ESP's file system: the first
// 32 bits of the file system UUID it derives from the partition UUID.
func espVolumeID(partUUID string) uint32 {
	fs, _ := hex.DecodeString(strings.ReplaceAll(deriveUUID(partUUID, []byte("file-system-uuid")), "-", ""))
	return binary.BigEndian.Uint32(fs)
}
