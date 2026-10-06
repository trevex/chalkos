package pki

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/plugin"
	"filippo.io/age/tag"
	"golang.org/x/crypto/ssh"
)

// ParseRecipient parses an age recipient as the age CLI does: native X25519 and post-quantum
// keys, tag recipients, SSH public keys, and recipients of age plugins, which run the plugin
// binary from PATH and talk to the user through ui.
func ParseRecipient(s string, ui *plugin.ClientUI) (age.Recipient, error) {
	switch {
	case strings.HasPrefix(s, "age1tag1") || strings.HasPrefix(s, "age1tagpq1"):
		return tag.ParseRecipient(s)
	case strings.HasPrefix(s, "age1pq1"):
		return age.ParseHybridRecipient(s)
	// Plugin recipients are age1<plugin name>1<data>.
	case strings.HasPrefix(s, "age1") && strings.Count(s, "1") > 1:
		return plugin.NewRecipient(s, ui)
	case strings.HasPrefix(s, "age1"):
		return age.ParseX25519Recipient(s)
	case strings.HasPrefix(s, "ssh-"):
		return agessh.ParseRecipient(s)
	}
	return nil, fmt.Errorf("unknown recipient type %q: want an age public key, an SSH public key, or an age plugin recipient", s)
}

// SSHPassphrase supplies the passphrase of an encrypted SSH private key when it is first needed.
type SSHPassphrase func() ([]byte, error)

// ParseIdentities parses an identity file: one or more native or plugin identities, one per line,
// or an SSH private key in PEM form. publicKey is the matching SSH public key (the .pub file),
// needed only for encrypted SSH keys in the old PEM format.
func ParseIdentities(data []byte, ui *plugin.ClientUI, passphrase SSHPassphrase, publicKey func() ([]byte, error)) ([]age.Identity, error) {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN")) {
		return parseSSHIdentity(data, passphrase, publicKey)
	}
	var ids []age.Identity
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var id age.Identity
		var err error
		switch {
		case strings.HasPrefix(line, "AGE-PLUGIN-"):
			id, err = plugin.NewIdentity(line, ui)
		case strings.HasPrefix(line, "AGE-SECRET-KEY-PQ-1"):
			id, err = age.ParseHybridIdentity(line)
		case strings.HasPrefix(line, "AGE-SECRET-KEY-1"):
			id, err = age.ParseX25519Identity(line)
		default:
			// The line is not echoed: it may be a secret in the wrong file.
			err = errors.New("not an age identity")
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		ids = append(ids, id)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errors.New("no identities found")
	}
	return ids, nil
}

func parseSSHIdentity(pemBytes []byte, passphrase SSHPassphrase, publicKey func() ([]byte, error)) ([]age.Identity, error) {
	id, err := agessh.ParseIdentity(pemBytes)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		pub := missing.PublicKey
		if pub == nil {
			data, err := publicKey()
			if err != nil {
				return nil, fmt.Errorf("the SSH key is encrypted and its public key is needed: %w", err)
			}
			if pub, _, _, _, err = ssh.ParseAuthorizedKey(data); err != nil {
				return nil, fmt.Errorf("parse the SSH public key: %w", err)
			}
		}
		id, err := agessh.NewEncryptedSSHIdentity(pub, pemBytes, passphrase)
		if err != nil {
			return nil, err
		}
		return []age.Identity{id}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parse the SSH private key: %w", err)
	}
	return []age.Identity{id}, nil
}
