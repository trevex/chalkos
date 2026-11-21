package secureboot

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/asn1"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testPolicy is an IPE policy as an image carries it.
const testPolicy = "policy_name=chalkos policy_version=0.5.0\nDEFAULT action=ALLOW\nDEFAULT op=KMODULE action=DENY\n"

// The signature is what the kernel's PKCS #7 parser takes for an IPE policy: a ContentInfo of
// SignedData whose content is data and carried inside, one signer without authenticated
// attributes, SHA-256 and RSA.
func TestSignAttachedStructure(t *testing.T) {
	k, _ := testKeys(t)
	der, err := SignAttached([]byte(testPolicy), k.DB)
	if err != nil {
		t.Fatal(err)
	}
	var ci contentInfo
	if rest, err := asn1.Unmarshal(der, &ci); err != nil || len(rest) > 0 || !ci.ContentType.Equal(oidSignedData) {
		t.Fatalf("not one ContentInfo of SignedData: %v, %d trailing bytes", err, len(rest))
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		t.Fatal(err)
	}
	if !sd.ContentInfo.ContentType.Equal(oidData) {
		t.Errorf("content type %v, want data", sd.ContentInfo.ContentType)
	}
	if len(sd.SignerInfos) != 1 {
		t.Fatalf("%d signers", len(sd.SignerInfos))
	}
	si := sd.SignerInfos[0]
	if len(si.AuthenticatedAttributes.FullBytes) != 0 {
		t.Error("the signer has authenticated attributes")
	}
	if !si.DigestAlgorithm.Algorithm.Equal(oidSHA256) || !si.IssuerAndSerial.of(k.DB.Cert) {
		t.Errorf("signer %+v", si)
	}
	digest := sha256.Sum256([]byte(testPolicy))
	if err := rsa.VerifyPKCS1v15(&k.DB.Key.PublicKey, crypto.SHA256, digest[:], si.EncryptedDigest); err != nil {
		t.Errorf("signature: %v", err)
	}
	got, err := AttachedContent(der)
	if err != nil || string(got) != testPolicy {
		t.Errorf("AttachedContent = %q, %v", got, err)
	}
}

func TestAttachedContentRefusesOtherData(t *testing.T) {
	k, _ := testKeys(t)
	der, err := SignAttached([]byte(testPolicy), k.DB)
	if err != nil {
		t.Fatal(err)
	}
	for name, der := range map[string][]byte{
		"empty":     nil,
		"trailing":  append(der, 0x01),
		"not pkcs7": {0x30, 0x03, 0x02, 0x01, 0x01},
	} {
		if _, err := AttachedContent(der); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// openssl, which the spike signed policies with, verifies the signature and gets the content back.
func TestSignAttachedOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not in PATH")
	}
	k, keys := testKeys(t)
	der, err := SignAttached([]byte(testPolicy), k.DB)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p7b, out := filepath.Join(dir, "chalkos.p7b"), filepath.Join(dir, "content")
	if err := os.WriteFile(p7b, der, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("openssl", "cms", "-verify", "-inform", "der", "-binary", "-in", p7b,
		"-certfile", filepath.Join(keys, "db.crt"), "-noverify", "-out", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("openssl cms -verify: %v\n%s", err, b)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, []byte(testPolicy)) {
		t.Errorf("openssl's content %q", got)
	}
}
