package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/plugin"
	"golang.org/x/term"

	"github.com/trevex/chalkos/pkg/pki"
)

// stringList is a flag that may be given several times.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// secretFlags say where the secrets file is and how to decrypt it.
type secretFlags struct {
	path       string
	identities stringList
}

func (s *secretFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&s.path, "secrets", "", "secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)")
	fs.Var(&s.identities, "identity", "age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)")
}

// loadSecrets reads the secrets file in whatever format it has. Age identities are read only when
// the file is encrypted.
func (a *app) loadSecrets(s secretFlags, flake string) (pki.Secrets, error) {
	path := s.path
	if path == "" {
		path = filepath.Join(flake, "secrets.age")
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			path = filepath.Join(flake, "secrets.json")
		}
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(a.stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return pki.Secrets{}, fmt.Errorf("read the secrets file: %w", err)
	}
	secrets, err := pki.ReadSecrets(data, func() ([]age.Identity, error) { return a.ageIdentities(s.identities) })
	if err != nil {
		return pki.Secrets{}, fmt.Errorf("%s: %w", path, err)
	}
	return secrets, nil
}

// ageIdentities parses the given identity files, or the default ones that exist.
func (a *app) ageIdentities(files []string) ([]age.Identity, error) {
	if len(files) == 0 {
		for _, f := range []string{
			filepath.Join(a.home, ".config", "chalkos", "age.key"),
			filepath.Join(a.home, ".ssh", "id_ed25519"),
			filepath.Join(a.home, ".ssh", "id_rsa"),
		} {
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
		if len(files) == 0 {
			return nil, errors.New("the secrets file is encrypted and no age identity was found; pass --identity")
		}
	}
	var ids []age.Identity
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		parsed, err := pki.ParseIdentities(data, a.pluginUI(), a.passphrase(f), func() ([]byte, error) {
			return os.ReadFile(f + ".pub")
		})
		if err != nil {
			return nil, fmt.Errorf("identity %s: %w", f, err)
		}
		ids = append(ids, parsed...)
	}
	return ids, nil
}

func (a *app) pluginUI() *plugin.ClientUI {
	return plugin.NewTerminalUI(func(format string, v ...any) {
		fmt.Fprintf(a.stderr, "chalkctl: "+format+"\n", v...)
	}, func(format string, v ...any) {
		fmt.Fprintf(a.stderr, "chalkctl: warning: "+format+"\n", v...)
	})
}

// passphrase asks for an SSH key's passphrase on the terminal.
func (a *app) passphrase(file string) pki.SSHPassphrase {
	return func() ([]byte, error) {
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return nil, fmt.Errorf("%s is encrypted and there is no terminal to ask for its passphrase", file)
		}
		defer tty.Close()
		fmt.Fprintf(tty, "Passphrase for %s: ", file)
		pass, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		return pass, err
	}
}

func (a *app) recoveryKey(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recovery-key", flag.ContinueOnError)
	var cf clusterFlags
	var sf secretFlags
	cf.register(fs)
	sf.register(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl recovery-key <node>")
	}
	c, err := a.loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	if _, err := c.node(pos[0]); err != nil {
		return err
	}
	secrets, err := a.loadSecrets(sf, cf.flake)
	if err != nil {
		return err
	}
	key, err := pki.RecoveryKey(secrets.RecoverySecret, c.manifest.Cluster.Name, pos[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, key)
	return nil
}

func (a *app) genSecrets(args []string) error {
	fs := flag.NewFlagSet("gen secrets", flag.ContinueOnError)
	var recipients stringList
	fs.Var(&recipients, "recipient", "age recipient to encrypt secrets.age to: an age public key, an SSH public key, or an age plugin recipient; may be repeated")
	plaintext := fs.Bool("plaintext", false, "write secrets.json unencrypted, for files protected by other means")
	out := fs.String("out", ".", "directory to write the files to")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if (len(recipients) == 0) == !*plaintext {
		return errors.New("gen secrets: pass --recipient (one or more) or --plaintext")
	}
	name := "secrets.age"
	if *plaintext {
		name = "secrets.json"
	}
	secretsPath := filepath.Join(*out, name)
	publicPath := filepath.Join(*out, "secrets.pub.json")
	// The secrets file is written once: a new one would orphan every installed node.
	for _, p := range []string{filepath.Join(*out, "secrets.age"), filepath.Join(*out, "secrets.json"), publicPath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s exists; the secrets are generated once per cluster", p)
		}
	}

	secrets, err := pki.GenerateSecrets(time.Now())
	if err != nil {
		return err
	}
	var data []byte
	if *plaintext {
		data, err = secrets.Encode()
	} else {
		var parsed []age.Recipient
		for _, r := range recipients {
			rec, err := pki.ParseRecipient(r, a.pluginUI())
			if err != nil {
				return err
			}
			parsed = append(parsed, rec)
		}
		data, err = secrets.Encrypt(parsed...)
	}
	if err != nil {
		return err
	}
	public, err := json.MarshalIndent(secrets.Public(), "", "  ")
	if err != nil {
		return err
	}
	if err := writeNew(secretsPath, data, 0o600); err != nil {
		return err
	}
	if err := writeNew(publicPath, append(public, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote %s and %s\n", secretsPath, publicPath)
	if *plaintext {
		fmt.Fprintf(a.stderr, "warning: %s holds the cluster's secrets unencrypted; protect it by other means, for example keep it out of version control with a .gitignore entry\n", secretsPath)
	}
	return nil
}

// writeNew writes a file that must not exist yet.
func writeNew(path string, data []byte, perm fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
