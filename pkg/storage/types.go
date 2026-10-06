package storage

import (
	"crypto/sha256"
	"encoding/hex"
)

// PartitionType returns the GPT type UUID of the chalkos partition with the label: a hash of
// the label, computed as the cluster definition renders it into repart definitions. STATE, VAR
// and every volume have their own type, so repart never assigns one's partition to another.
func PartitionType(label string) string {
	sum := sha256.Sum256([]byte("chalkos-partition-type:" + label))
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
