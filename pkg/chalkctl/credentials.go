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
// --config names, else the secrets file --secrets names, else the client file $CHALKOSCONFIG
// names, else ~/.config/chalkos/config.
func (a *app) loadCredentials(ctx context.Context, s secretFlags, config string) (*credentials, error) {
	if config != "" {
		return a.configCredentials(config)
	}
	if s.path != "" {
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
// key, the OS CA, and the nodes' addresses.
type configNewFlags struct {
	cluster         clusterFlags
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
	}, func(a *app, ctx context.Context, pos []string) error { return a.configNew(ctx, f, pos) })
	fs := cmd.Flags()
	f.cluster.register(fs)
	f.secrets.register(fs)
	fs.StringVar(&f.name, "name", "", "the user's name, which the certificate carries")
	fs.StringVar(&f.role, "role", "", "the role the certificate grants: admin, operator or reader")
	fs.DurationVar(&f.ttl, "ttl", 8760*time.Hour, "validity of the certificate")
	fs.StringVar(&f.out, "out", "", "file to write (default ~/.config/chalkos/config)")
	fs.BoolVar(&f.force, "force", false, "replace an existing file")
	return cmd
}

func (a *app) configNew(ctx context.Context, f configNewFlags, pos []string) error {
	cf, sf := f.cluster, f.secrets
	if len(pos) != 0 || f.name == "" || f.role == "" {
		return errors.New("usage: chalkctl config new --name NAME --role admin|operator|reader [--ttl 8760h] [--out FILE] [--force]")
	}
	c, err := loadCluster(cf)
	if err != nil {
		return err
	}
	if err := c.requireNodes("config new"); err != nil {
		return err
	}
	secrets, err := a.loadSecrets(ctx, sf)
	if err != nil {
		return err
	}
	// A cluster definition's client file names the nodes' addresses; an inventory's nodes are
	// reached at the inventory's.
	nodes := map[string]string{}
	if c.manifest != nil {
		for _, node := range c.nodeNames() {
			if addrs := c.staticAddresses(node); len(addrs) > 0 {
				nodes[node] = addrs[0]
			}
		}
	}
	config, err := client.NewConfig(secrets.ClientCA(), secrets.OSCABundle(), c.name, f.name, f.role, f.ttl, nodes, time.Now())
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
