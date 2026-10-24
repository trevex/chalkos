package storage

import (
	"crypto/sha256"
	"encoding/hex"
)

// UUIDOf hashes input into the shape of a UUID, so the same input always gives the same UUID:
// the partition types, which the image derives too, and the disks' seeds installed nodes hold.
func UUIDOf(input string) string {
	sum := sha256.Sum256([]byte(input))
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// PartitionType returns the GPT type UUID of the chalkos partition with the label: a hash of
// the label, as the image's and the node's repart definitions name it. STATE, VAR
// and every volume have their own type, so repart never assigns one's partition to another.
func PartitionType(label string) string { return UUIDOf("chalkos-partition-type:" + label) }
