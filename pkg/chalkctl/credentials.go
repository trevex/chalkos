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

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/sdk"
)

// credentials are what chalkctl authenticates to nodes with and verifies them by: an admin
// certificate it issues itself from the secrets file, or a client file's certificate.
type credentials struct {
	// secrets is set when the credentials come from the secrets file.
	secrets *clustersecrets.Secrets
	// config is set, with the path it was read from, when they come from a client file.
	config     *client.Config
	configPath string
	cert       *tls.Certificate
	// osCA holds the PEM certificates of the OS CAs nodes are verified by.
	osCA string
}

// role is the role the credentials grant: admin for the secrets file, else the role the client
// file's certificate names, empty for one that names none.
func (c *credentials) role() pki.Role {
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
	role, _ := pki.RoleOf(leaf)
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

// secretCredentials are the credentials of the secrets file, with an admin certificate the OS CA
// issues chalkctl for this run.
func secretCredentials(s clustersecrets.Secrets) (*credentials, error) {
	cert, err := sdk.AdminCertificate(s, "chalkctl", time.Now())
	if err != nil {
		return nil, err
	}
	return &credentials{secrets: &s, cert: cert, osCA: s.OSCABundle()}, nil
}

// loadSecretCredentials reads the secrets file for commands that need it.
func (a *app) loadSecretCredentials(ctx context.Context, s layer.SecretsFlags) (*credentials, error) {
	secrets, err := a.loadSecrets(ctx, s)
	if err != nil {
		return nil, err
	}
	return secretCredentials(secrets)
}

// expiresSoon is whether a client file's certificate is in the last third of its lifetime, when
// chalkctl warns that it expires.
func expiresSoon(leaf *x509.Certificate, now time.Time) bool {
	return leaf.NotAfter.Sub(now) < leaf.NotAfter.Sub(leaf.NotBefore)/3
}

// loadCredentials picks the credentials of the commands a client file may run: --config's client
// file or --secrets's secrets file, not both; else $CHALKOSCONFIG's client file, else
// ~/.config/chalkos/config.
func (a *app) loadCredentials(ctx context.Context, s layer.SecretsFlags, config string) (*credentials, error) {
	switch {
	case config != "" && s.Path != "":
		return nil, cli.Usagef("pass --secrets or --config, not both")
	case config != "":
		return a.configCredentials(config)
	case s.Path != "":
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

// defaultConfigPath is where chalkctl looks for a client file and chalkctl client new writes one.
func defaultConfigPath(home string) string {
	return filepath.Join(home, ".config", "chalkos", "config")
}

// configCredentials reads a client file, refuses it once its certificate expired and warns in
// the last third of its lifetime.
func (a *app) configCredentials(path string) (*credentials, error) {
	c, err := client.ReadConfig(path)
	if err != nil {
		return nil, err
	}
	a.warnReadable(path, "a private key")
	leaf, err := c.Leaf()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	expires := leaf.NotAfter.UTC().Format(time.DateOnly)
	switch {
	case !now.Before(leaf.NotAfter):
		return nil, fmt.Errorf("the certificate of %s expired on %s; issue a new one with chalkctl client new", path, expires)
	case expiresSoon(leaf, now):
		fmt.Fprintf(a.stderr, "chalkctl: warning: the certificate of %s expires on %s; issue a new one with chalkctl client new\n", path, expires)
	}
	cert, err := c.TLSCertificate()
	if err != nil {
		return nil, err
	}
	return &credentials{config: &c, configPath: path, cert: cert, osCA: c.OSCA}, nil
}

// clientValidity is how long the certificate of a client file lives by default, by role: the
// file is issued again from the secrets file at any time, so the more a role may do, the
// shorter. A reader's lasts a year, for dashboards.
var clientValidity = map[pki.Role]time.Duration{
	pki.RoleAdmin:    30 * 24 * time.Hour,
	pki.RoleOperator: 90 * 24 * time.Hour,
	pki.RoleReader:   365 * 24 * time.Hour,
}

type clientNewFlags struct {
	secrets         layer.SecretsFlags
	name, role, out string
	ttl             time.Duration
	overwrite       bool
}

func (a *app) clientNewCommand() *cobra.Command {
	var f clientNewFlags
	cmd := a.command(&cobra.Command{
		Use:         "new",
		Short:       "Write a client file, which operates the cluster without the secrets file",
		Long:        clientNewLong,
		Example:     clientNewExample,
		Args:        cli.NoArgs,
		Annotations: layer.Meta{Credentials: layer.SecretsFile}.Annotations(),
	}, func(a *app, ctx context.Context, pos []string) error { return a.clientNew(ctx, f) })
	fs := cmd.Flags()
	f.secrets.Register(fs)
	fs.StringVar(&f.name, "name", "", "the user's name, which the certificate carries")
	fs.StringVar(&f.role, "role", "", "the role the certificate grants: admin, operator or reader")
	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("role")
	fs.DurationVar(&f.ttl, "ttl", 0, "validity of the certificate (default 720h for an admin, 2160h for an operator, 8760h for a reader)")
	fs.StringVar(&f.out, "out", "", "file to write (default ~/.config/chalkos/config)")
	fs.BoolVar(&f.overwrite, "overwrite", false, "replace an existing file")
	return cmd
}

// clientNew issues a client file from the secrets file: a certificate of the role for a new
// key, and the OS CA.
func (a *app) clientNew(ctx context.Context, f clientNewFlags) error {
	if f.name == "" || f.role == "" {
		return cli.Usagef("client new needs a --name and a --role")
	}
	secrets, err := a.loadSecrets(ctx, f.secrets)
	if err != nil {
		return err
	}
	ttl := f.ttl
	if ttl == 0 {
		ttl = clientValidity[pki.Role(f.role)]
	}
	config, err := client.NewConfig(f.name, pki.Role(f.role), secrets.ClientCA(), secrets.OSCABundle(), ttl, time.Now())
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
	if f.overwrite {
		err = atomicfile.WriteFile(path, data, 0o600)
	} else if _, statErr := os.Stat(path); statErr == nil {
		return fmt.Errorf("%s exists; pass --overwrite to replace it", path)
	} else {
		err = atomicfile.WriteNew(path, data, 0o600)
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
