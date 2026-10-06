package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
	"filippo.io/age/plugin"
	"golang.org/x/crypto/ssh"
)

func generate(t *testing.T) Secrets {
	t.Helper()
	s, err := GenerateSecrets(now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func noIdentities() ([]age.Identity, error) { return nil, errors.New("asked for an identity") }

func TestGenerateSecrets(t *testing.T) {
	s := generate(t)
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	admin, _, err := s.Admin.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := Role(admin); role != RoleAdmin {
		t.Errorf("admin certificate grants %q", role)
	}
	pub := s.Public()
	if pub.OSCA.Key != "" || pub.OSCA.Certificate != s.OSCA.Certificate {
		t.Errorf("public part = %+v, want the CA certificate only", pub)
	}
}

func TestReadSecretsPlaintext(t *testing.T) {
	s := generate(t)
	data, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadSecrets(data, noIdentities)
	if err != nil {
		t.Fatal(err)
	}
	if got.OSCA != s.OSCA || got.Admin != s.Admin || !bytes.Equal(got.RecoverySecret, s.RecoverySecret) {
		t.Error("plaintext round trip changed the secrets")
	}
}

func TestReadSecretsAge(t *testing.T) {
	s := generate(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ParseRecipient(id.Recipient().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := s.Encrypt(recipient)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("PRIVATE KEY")) {
		t.Fatal("the encrypted file contains a plaintext key")
	}
	var armored bytes.Buffer
	w := armor.NewWriter(&armored)
	w.Write(encrypted)
	w.Close()

	ids, err := ParseIdentities([]byte("# key\n"+id.String()+"\n"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"binary": encrypted, "armored": armored.Bytes()} {
		got, err := ReadSecrets(data, func() ([]age.Identity, error) { return ids, nil })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.OSCA != s.OSCA || !bytes.Equal(got.RecoverySecret, s.RecoverySecret) {
			t.Errorf("%s: round trip changed the secrets", name)
		}
	}

	other, _ := age.GenerateX25519Identity()
	if _, err := ReadSecrets(encrypted, func() ([]age.Identity, error) { return []age.Identity{other}, nil }); err == nil {
		t.Error("decrypted with an identity that is not a recipient")
	}
}

func TestReadSecretsSSH(t *testing.T) {
	s := generate(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ParseRecipient(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := s.Encrypt(recipient)
	if err != nil {
		t.Fatal(err)
	}

	plainKey, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	protected, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	passphrase := func() ([]byte, error) { asked++; return []byte("hunter2"), nil }
	for name, block := range map[string]*pem.Block{"plain": plainKey, "encrypted": protected} {
		ids, err := ParseIdentities(pem.EncodeToMemory(block), nil, passphrase, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := ReadSecrets(encrypted, func() ([]age.Identity, error) { return ids, nil }); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if asked != 1 {
		t.Errorf("asked for the passphrase %d times, want once", asked)
	}
}

func TestReadSecretsRejects(t *testing.T) {
	s := generate(t)
	broken := s
	broken.RecoverySecret = broken.RecoverySecret[:8]
	short, _ := broken.Encode()
	for name, data := range map[string][]byte{
		"garbage":       []byte("not secrets"),
		"short secret":  short,
		"other version": []byte(`{"version": 2}`),
	} {
		if _, err := ReadSecrets(data, noIdentities); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseRecipientRejectsUnknown(t *testing.T) {
	if _, err := ParseRecipient("github:someone", nil); err == nil {
		t.Error("accepted an unknown recipient type")
	}
}

func TestParseIdentitiesDoesNotEchoLines(t *testing.T) {
	_, err := ParseIdentities([]byte("my-secret-password\n"), nil, nil, nil)
	if err == nil || strings.Contains(err.Error(), "my-secret-password") {
		t.Errorf("err = %v, want an error that does not repeat the line", err)
	}
}

func TestParseRecipientRejectsSecretKey(t *testing.T) {
	input := "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	_, err := ParseRecipient(input, nil)
	if err == nil {
		t.Fatal("accepted a secret key as a recipient")
	}
	if strings.Contains(err.Error(), input) {
		t.Errorf("err = %v, want an error that does not repeat the input", err)
	}
	// Case-insensitive, as age itself prints the prefix upper-case only.
	if _, err := ParseRecipient(strings.ToLower(input), nil); err == nil {
		t.Error("accepted a lower-case secret key as a recipient")
	}
}

func TestParseRecipientPluginRequiresUI(t *testing.T) {
	recipient := plugin.EncodeRecipient("yubikey", []byte{1, 2, 3, 4})
	if _, err := ParseRecipient(recipient, nil); err == nil {
		t.Error("accepted a plugin recipient without a UI")
	}
}

func TestParseIdentitiesPluginRequiresUI(t *testing.T) {
	identity := plugin.EncodeIdentity("yubikey", []byte{1, 2, 3, 4})
	if _, err := ParseIdentities([]byte(identity+"\n"), nil, nil, nil); err == nil {
		t.Error("accepted a plugin identity without a UI")
	}
}

func TestParseIdentitiesSSHNilPassphrase(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseIdentities(pem.EncodeToMemory(protected), nil, nil, nil); err == nil {
		t.Error("accepted an encrypted SSH key without a passphrase callback")
	}
}

func TestParseIdentitiesSSHNilPublicKeyCallback(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), []byte("hunter2"), x509.PEMCipherAES128)
	if err != nil {
		t.Fatal(err)
	}
	passphrase := func() ([]byte, error) { return []byte("hunter2"), nil }
	if _, err := ParseIdentities(pem.EncodeToMemory(block), nil, passphrase, nil); err == nil {
		t.Error("accepted an SSH key with no embedded public key and no public key callback")
	}
}

func TestReadSecretsArmoredLeadingWhitespace(t *testing.T) {
	s := generate(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ParseRecipient(id.Recipient().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := s.Encrypt(recipient)
	if err != nil {
		t.Fatal(err)
	}
	var armored bytes.Buffer
	w := armor.NewWriter(&armored)
	w.Write(encrypted)
	w.Close()
	padded := append([]byte("\n  "), armored.Bytes()...)

	ids, err := ParseIdentities([]byte(id.String()+"\n"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecrets(padded, func() ([]age.Identity, error) { return ids, nil }); err != nil {
		t.Errorf("armored file with leading whitespace did not decrypt: %v", err)
	}
}

func TestSecretsValidateRejectsNonCAOSCA(t *testing.T) {
	s := generate(t)
	leaf, err := SelfSigned("not-a-ca", now)
	if err != nil {
		t.Fatal(err)
	}
	s.OSCA = leaf
	if err := s.Validate(); err == nil {
		t.Error("accepted an osCA that is not a CA certificate")
	}
}

func TestSecretsValidateRejectsForeignAdmin(t *testing.T) {
	s := generate(t)
	other, err := NewCA("other CA", now)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := IssueClient(other, "admin", RoleAdmin, now)
	if err != nil {
		t.Fatal(err)
	}
	s.Admin = admin
	if err := s.Validate(); err == nil {
		t.Error("accepted an admin certificate issued by a different CA")
	}
}

func TestSecretsStringRedacted(t *testing.T) {
	s := generate(t)
	out := fmt.Sprintf("%v %+v %#v", s, s, s)
	if strings.Contains(out, "PRIVATE KEY") {
		t.Errorf("formatted secrets contain a private key: %s", out)
	}
	if bytes.Contains([]byte(out), s.RecoverySecret) {
		t.Error("formatted secrets contain the raw recovery secret")
	}
	if strings.Contains(out, hex.EncodeToString(s.RecoverySecret)) {
		t.Error("formatted secrets contain the recovery secret in hex")
	}
	if strings.Contains(out, base64.StdEncoding.EncodeToString(s.RecoverySecret)) {
		t.Error("formatted secrets contain the recovery secret in base64")
	}
}
