package modulepolicy

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

const root = "ed2cf51fb5061f0886c29887e768028cf6431c4b18ab02ada4650bae1cfd05a2"

var text = "policy_name=chalkos policy_version=0.4.0\n" +
	"DEFAULT action=ALLOW\n" +
	"DEFAULT op=KMODULE action=DENY\n" +
	"op=KMODULE boot_verified=TRUE action=ALLOW\n" +
	"op=KMODULE dmverity_roothash=sha256:" + root + " action=ALLOW\n"

func padded(b []byte) []byte { return append(bytes.Clone(b), make([]byte, SlotSize-len(b))...) }

func TestTrimUnsigned(t *testing.T) {
	signed, blob, err := Trim(padded([]byte(text)))
	if err != nil || signed || string(blob) != text {
		t.Errorf("Trim = %v, %q, %v", signed, blob, err)
	}
}

func TestTrimSigned(t *testing.T) {
	// A DER SEQUENCE of 3 bytes, then the slot's NULs.
	der := []byte{0x30, 0x03, 0x02, 0x01, 0x05}
	signed, blob, err := Trim(padded(der))
	if err != nil || !signed || !bytes.Equal(blob, der) {
		t.Errorf("Trim = %v, %x, %v", signed, blob, err)
	}
}

func TestTrimRefuses(t *testing.T) {
	for name, slot := range map[string][]byte{
		"empty":            make([]byte, SlotSize),
		"DER past the end": padded([]byte{0x30, 0x82, 0x7f, 0xff}),
		"data after DER":   padded([]byte{0x30, 0x00, 0x01}),
		"data after text":  padded([]byte("policy_name=chalkos\n\x00x")),
	} {
		if _, _, err := Trim(slot); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestRootHash(t *testing.T) {
	got, err := RootHash([]byte(text))
	if err != nil || hex.EncodeToString(got) != root {
		t.Errorf("RootHash = %x, %v", got, err)
	}
	for _, bad := range []string{"DEFAULT action=ALLOW\n", strings.Replace(text, "sha256:", "sha1:", 1)} {
		if _, err := RootHash([]byte(bad)); err == nil {
			t.Errorf("RootHash(%q): no error", bad)
		}
	}
}
