package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/pflag"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/storage"
)

// fallbackSecret returns the secret enrolled as the second keyslot of the node's encrypted
// volumes: its derived recovery key, or the operator's password. A password typed on the
// terminal is asked twice.
func (a *app) fallbackSecret(ctx context.Context, t *target, fallback, passwordFile string) (string, error) {
	switch fallback {
	case storage.FallbackNone:
		return "", nil
	case storage.FallbackRecoveryKey:
		if t.creds.secrets == nil {
			return "", fmt.Errorf("node %s unlocks with its recovery key when its TPM fails, which only the secrets file gives; pass --secrets", t.name)
		}
		return clustersecrets.RecoveryKey(t.secrets.RecoverySecret, t.name)
	case storage.FallbackPassword:
		if passwordFile != "" {
			data, err := os.ReadFile(passwordFile)
			if err != nil {
				return "", err
			}
			// Only the line ending that ends the file's line, LF or CRLF; the rest belongs to
			// the password.
			password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
			if password == "" {
				return "", fmt.Errorf("%s is empty", passwordFile)
			}
			return password, nil
		}
		return a.askPassword(ctx, t.name)
	}
	return "", fmt.Errorf("node %s has the unknown fallback %q", t.name, fallback)
}

func (a *app) askPassword(ctx context.Context, node string) (string, error) {
	read := func(prompt string) (string, error) {
		p, err := a.readSecret(ctx, prompt)
		if errors.Is(err, errNoTerminal) {
			return "", fmt.Errorf("node %s unlocks with a password when its TPM fails and there is no terminal to ask for it; pass --password-file", node)
		}
		return string(p), err
	}
	password, err := read(fmt.Sprintf("Password that unlocks %s when its TPM fails: ", node))
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("the password is empty")
	}
	again, err := read("Repeat the password: ")
	if err != nil {
		return "", err
	}
	if again != password {
		return "", errors.New("the passwords differ")
	}
	return password, nil
}

// fallbackFor returns the fallback secret when delivering the config enrolls it: on an encrypted
// volume the node does not have yet, or on the volume reset recreates. section is the storage the
// config gives the node; the node's status says which volumes it has. A password typed on the
// terminal is confirmed: the node checks it against its existing fallback keyslot only when it
// has one.
func (a *app) fallbackFor(ctx context.Context, conn *client.Conn, t *target, section storage.Section, passwordFile, reset string) (string, error) {
	// Without a fallback the node is not asked for its volumes.
	if section.Fallback == storage.FallbackNone {
		return "", nil
	}
	resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return "", fmt.Errorf("ask %s for its volumes: %w", t.name, err)
	}
	return a.fallbackOf(ctx, resp.Msg, t, section, passwordFile, reset)
}

// fallbackOf is fallbackFor with the node's status at hand.
func (a *app) fallbackOf(ctx context.Context, st *nodev1.StatusResponse, t *target, section storage.Section, passwordFile, reset string) (string, error) {
	var existing []string
	for _, v := range st.Volumes {
		existing = append(existing, v.Name)
	}
	if !fallbackNeeded(section, existing, reset) {
		return "", nil
	}
	return a.fallbackSecret(ctx, t, section.Fallback, passwordFile)
}

// fallbackNeeded tells whether the section enrolls the fallback keyslot on a volume besides the
// existing ones, or on the volume reset recreates.
func fallbackNeeded(section storage.Section, existing []string, reset string) bool {
	if section.Fallback == storage.FallbackNone {
		return false
	}
	for name, v := range section.Volumes {
		if v.Encryption == storage.EncryptionTPM2 && (name == reset || !slices.Contains(existing, name)) {
			return true
		}
	}
	return false
}

// registerPasswordFile registers --password-file.
func registerPasswordFile(fs *pflag.FlagSet, p *string) {
	fs.StringVar(p, "password-file", "", "file holding the password of a node whose fallback is a password")
}
