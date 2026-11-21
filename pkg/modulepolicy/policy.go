// Package modulepolicy loads the image's IPE policy, which lets the kernel load modules from the
// image's verity store and its initrd alone, and reports whether the kernel enforces it. It
// imports the standard library alone, as the initrd's loader links it.
package modulepolicy

import (
	"bytes"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
)

const (
	// Name is the policy's policy_name.
	Name = "chalkos"
	// PolicyFile is the policy's file in the initrd, relative to its root: the UKI's first
	// initrd, which the image build writes and chalkbuild sign signs in place.
	PolicyFile = "etc/chalkos/module-policy"
	// SlotSize is the size of PolicyFile: the policy, unsigned text or a DER PKCS #7 signature
	// over it, followed by NUL bytes. modules/node/base/module-policy.nix writes the same size.
	SlotSize = 16384
	// RecordFile is where the initrd's loader records what it did, below /run, which the boot
	// keeps after switch-root.
	RecordFile = "chalkos/module-policy.json"
)

// Trim returns the policy a slot holds without the NULs that fill it: the DER signature when
// signed is true, the unsigned text otherwise.
func Trim(slot []byte) (signed bool, blob []byte, err error) {
	if len(slot) == 0 || slot[0] == 0 {
		return false, nil, errors.New("the module policy's file is empty")
	}
	if slot[0] == 0x30 {
		var v asn1.RawValue
		rest, err := asn1.Unmarshal(slot, &v)
		if err != nil {
			return false, nil, fmt.Errorf("the signed module policy: %w", err)
		}
		if len(bytes.TrimRight(rest, "\x00")) > 0 {
			return false, nil, errors.New("the signed module policy is followed by data")
		}
		return true, v.FullBytes, nil
	}
	n := bytes.IndexByte(slot, 0)
	if n < 0 {
		n = len(slot)
	}
	if len(bytes.TrimRight(slot[n:], "\x00")) > 0 {
		return false, nil, errors.New("the module policy's text is followed by data")
	}
	return false, slot[:n], nil
}

// rootHashRE matches the rule that allows the image's store.
var rootHashRE = regexp.MustCompile(`(?m)^op=KMODULE dmverity_roothash=sha256:([0-9a-f]{64}) action=ALLOW$`)

// RootHash is the verity root hash of the store the policy text allows modules from.
func RootHash(text []byte) ([]byte, error) {
	m := rootHashRE.FindAllSubmatch(text, -1)
	if len(m) != 1 {
		return nil, fmt.Errorf("the module policy allows %d stores, not one", len(m))
	}
	return hex.DecodeString(string(m[0][1]))
}
