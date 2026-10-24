package chalkctl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/pki"
)

// credentials are what chalkctl authenticates to nodes with and verifies them by: an admin
// certificate it issues itself from the secrets file, or a client file's certificate.
type credentials struct {
	// secrets is set when the credentials come from the secrets file.
	secrets *pki.Secrets
	// config is set, with the path it was read from, when they come from a client file.
	config     *client.Config
	configPath string
	cert       *tls.Certificate
	// osCA holds the PEM certificates of the OS CAs nodes are verified by.
	osCA string
}

// role is the role the credentials grant: admin for the secrets file, else the role the client
// file's certificate names, empty for one that names none.
func (c *credentials) role() string {
	if c.secrets != nil {
		return pki.RoleAdmin
	}
	if c.config == nil {
		return ""
	}
	leaf, err := c.config.Leaf()
	if err != nil {
		return ""
	}
	role, _ := pki.Role(leaf)
	return role
}

// roots holds the OS CAs.
func (c *credentials) roots() (*x509.CertPool, error) {
	pool, err := pki.BundlePool(c.osCA)
	if err != nil {
		return nil, fmt.Errorf("the OS CA: %w", err)
	}
	return pool, nil
}

// adminValidity is how long the admin certificate chalkctl issues itself from the secrets file
// lives: it serves one run and is never stored.
const adminValidity = time.Hour

// adminCertificate issues chalkctl an admin certificate from the OS CA for this run.
func adminCertificate(s pki.Secrets) (*tls.Certificate, error) {
	ck, err := pki.IssueClient(s.OSCA, "chalkctl", pki.RoleAdmin, adminValidity, time.Now())
	if err != nil {
		return nil, fmt.Errorf("issue an admin certificate: %w", err)
	}
	cert, err := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

// secretCredentials are the credentials of the secrets file.
func secretCredentials(s pki.Secrets) (*credentials, error) {
	cert, err := adminCertificate(s)
	if err != nil {
		return nil, err
	}
	return &credentials{secrets: &s, cert: cert, osCA: s.OSCABundle()}, nil
}

// loadSecretCredentials reads the secrets file for commands that need it.
func (a *app) loadSecretCredentials(ctx context.Context, s secretFlags) (*credentials, error) {
	secrets, err := a.loadSecrets(ctx, s)
	if err != nil {
		return nil, err
	}
	return secretCredentials(secrets)
}

// configExpiryWarning is how long before its certificate expires chalkctl warns about a client
// file.
const configExpiryWarning = 30 * 24 * time.Hour

// loadCredentials picks the credentials of commands a client file may run: the client file
// --config names, or the secrets file --secrets names, but not both, which would leave one
// unread; else the client file $CHALKOSCONFIG names, else ~/.config/chalkos/config.
func (a *app) loadCredentials(ctx context.Context, s secretFlags, config string) (*credentials, error) {
	switch {
	case config != "" && s.path != "":
		return nil, cli.Usagef("pass --secrets or --config, not both")
	case config != "":
		return a.configCredentials(config)
	case s.path != "":
		return a.loadSecretCredentials(ctx, s)
	}
	if path := os.Getenv("CHALKOSCONFIG"); path != "" {
		return a.configCredentials(path)
	}
	path := defaultConfigPath(a.home)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("there is no client file at %s; pass --config or --secrets", path)
	}
	return a.configCredentials(path)
}

// defaultConfigPath is where chalkctl looks for a client file and chalkctl config new writes one.
func defaultConfigPath(home string) string {
	return filepath.Join(home, ".config", "chalkos", "config")
}

// configCredentials reads a client file, refuses it once its certificate expired and warns in
// the last 30 days before.
func (a *app) configCredentials(path string) (*credentials, error) {
	c, err := client.ReadConfig(path)
	if err != nil {
		return nil, err
	}
	leaf, err := c.Leaf()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	expires := leaf.NotAfter.UTC().Format(time.DateOnly)
	switch {
	case !now.Before(leaf.NotAfter):
		return nil, fmt.Errorf("the certificate of %s expired on %s; issue a new one with chalkctl config new", path, expires)
	case leaf.NotAfter.Sub(now) < configExpiryWarning:
		fmt.Fprintf(a.stderr, "chalkctl: warning: the certificate of %s expires on %s; issue a new one with chalkctl config new\n", path, expires)
	}
	cert, err := c.TLSCertificate()
	if err != nil {
		return nil, err
	}
	return &credentials{config: &c, configPath: path, cert: cert, osCA: c.OSCA}, nil
}

// configNew issues a client file from the secrets file: a certificate of the role for a new
// key, and the OS CA.
type configNewFlags struct {
	secrets         secretFlags
	name, role, out string
	ttl             time.Duration
	force           bool
}

func (a *app) configNewCommand() *cobra.Command {
	var f configNewFlags
	cmd := a.command(&cobra.Command{
		Use:     "new",
		Short:   "Write a client file, which operates the cluster without the secrets file",
		Long:    configNewLong,
		Example: configNewExample,
		Args:    cli.NoArgs,
	}, func(a *app, ctx context.Context, pos []string) error { return a.configNew(ctx, f) })
	fs := cmd.Flags()
	f.secrets.register(fs)
	fs.StringVar(&f.name, "name", "", "the user's name, which the certificate carries")
	fs.StringVar(&f.role, "role", "", "the role the certificate grants: admin, operator or reader")
	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("role")
	fs.DurationVar(&f.ttl, "ttl", 8760*time.Hour, "validity of the certificate")
	fs.StringVar(&f.out, "out", "", "file to write (default ~/.config/chalkos/config)")
	fs.BoolVar(&f.force, "force", false, "replace an existing file")
	return cmd
}

func (a *app) configNew(ctx context.Context, f configNewFlags) error {
	if f.name == "" || f.role == "" {
		return cli.Usagef("config new needs a --name and a --role")
	}
	secrets, err := a.loadSecrets(ctx, f.secrets)
	if err != nil {
		return err
	}
	config, err := client.NewConfig(secrets.ClientCA(), secrets.OSCABundle(), f.name, f.role, f.ttl, time.Now())
	if err != nil {
		return err
	}
	data, err := config.Encode()
	if err != nil {
		return err
	}
	path := f.out
	if path == "" {
		path = defaultConfigPath(a.home)
	}
	// The file holds the user's private key: a directory created for it is its owner's alone.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// The file holds the user's private key.
	if f.force {
		err = install.WriteFile(path, data, 0o600)
	} else if _, statErr := os.Stat(path); statErr == nil {
		return fmt.Errorf("%s exists; pass --force to replace it", path)
	} else {
		err = writeNew(path, data, 0o600)
	}
	if err != nil {
		return err
	}
	leaf, err := config.Leaf()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote %s for %s with the %s role; its certificate expires on %s\n", path, f.name, f.role, leaf.NotAfter.UTC().Format(time.DateOnly))
	return nil
}
