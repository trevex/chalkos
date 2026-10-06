package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
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
