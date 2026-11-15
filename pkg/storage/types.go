package storage

import (
	"crypto/sha256"
	"encoding/hex"
)

// UUIDOf hashes input into the shape of a UUID, so the same input always gives the same UUID:
// the partition types, which the image derives too, and the disks' seeds installed nodes hold.
func UUIDOf(input string) string {
	sum := sha256.Sum256([]byte(input))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// PartitionType is the GPT type of the chalkos partition with the label, a hash of it as the
// image's and the node's repart definitions name it, so repart never confuses two partitions.
func PartitionType(label string) string { return UUIDOf("chalkos-partition-type:" + label) }
