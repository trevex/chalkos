package imagesign

import (
	"bytes"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/modulepolicy"
	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

var policyRoot = strings.Repeat("ab", 32)

func policyText(root string) []byte {
	return []byte("policy_name=chalkos policy_version=0.5.0\nDEFAULT action=ALLOW\nDEFAULT op=KMODULE action=DENY\n" +
		"op=KMODULE boot_verified=TRUE action=ALLOW\nop=KMODULE dmverity_roothash=sha256:" + root + " action=ALLOW\n")
}

// loadSigner returns a db key pair and the path of its certificate.
func loadSigner(t *testing.T) (secureboot.KeyPair, string) {
	t.Helper()
	key, cert, _ := imagetest.Signer(t, t.TempDir(), "db")
	s, err := secureboot.LoadSigner(key, cert)
	if err != nil {
		t.Fatal(err)
	}
	return s, cert
}

// slotOf returns the policy slot of a UKI and whether it holds a signature.
func slotOf(t *testing.T, data []byte) (bool, []byte) {
	t.Helper()
	off, size, ok, err := uki.PolicySlot(bytes.NewReader(data))
	if err != nil || !ok {
		t.Fatalf("PolicySlot: %v, %v", ok, err)
	}
	signed, blob, err := modulepolicy.Trim(data[off : off+size])
	if err != nil {
		t.Fatal(err)
	}
	return signed, blob
}

func TestSignUKISignsThePolicy(t *testing.T) {
	signer, cert := loadSigner(t)
	data := ukitest.PolicyUKI(policyText(policyRoot), modulepolicy.SlotSize, policyRoot)
	signed, err := signUKI(data, signer)
	if err != nil {
		t.Fatal(err)
	}
	isSigned, blob := slotOf(t, signed)
	if !isSigned {
		t.Fatal("the policy is still unsigned")
	}
	if content, err := secureboot.AttachedContent(blob); err != nil || !bytes.Equal(content, policyText(policyRoot)) {
		t.Errorf("signed content %q, %v", content, err)
	}
	if err := signedBy(t, signed, cert); err != nil {
		t.Errorf("the UKI is not signed: %v", err)
	}
}

// A policy signed before, as in an image directory signed twice, is signed again from its text.
func TestSignPolicyAgain(t *testing.T) {
	signer, _ := loadSigner(t)
	data := ukitest.PolicyUKI(policyText(policyRoot), modulepolicy.SlotSize, policyRoot)
	off, size, _, err := uki.PolicySlot(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	once, err := signPolicy(data, off, size, signer)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := signPolicy(once, off, size, signer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(once, twice) {
		t.Error("signing a signed policy again changed it")
	}
}

func TestSignUKIRefusesAnotherStore(t *testing.T) {
	signer, _ := loadSigner(t)
	data := ukitest.PolicyUKI(policyText(strings.Repeat("cd", 32)), modulepolicy.SlotSize, policyRoot)
	if _, err := signUKI(data, signer); err == nil || !strings.Contains(err.Error(), "store") {
		t.Errorf("err = %v", err)
	}
}

func TestSignUKIWithoutPolicy(t *testing.T) {
	signer, _ := loadSigner(t)
	data := ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos"}, "usrhash="+policyRoot)
	if _, err := signUKI(data, signer); err != nil {
		t.Errorf("a UKI without a policy: %v", err)
	}
}
