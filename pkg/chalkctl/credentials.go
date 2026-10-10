package chalkctl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/manifest"
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
func (a *app) loadSecretCredentials(ctx context.Context, s secretFlags, flake string) (*credentials, error) {
	secrets, err := a.loadSecrets(ctx, s, flake)
	if err != nil {
		return nil, err
	}
	return secretCredentials(secrets)
}

// configExpiryWarning is how long before its certificate expires chalkctl warns about a client
// file.
const configExpiryWarning = 30 * 24 * time.Hour

// loadCredentials picks the credentials of commands a client file may run: the client file
// --config names, else the secrets file --secrets names; else the client file $CHALKOSCONFIG
// names, so a lab's client file works in the lab's flake directory; else the secrets file found
// in the flake directory; else ~/.config/chalkos/config.
func (a *app) loadCredentials(ctx context.Context, s secretFlags, config, flake string) (*credentials, error) {
	if config != "" {
		return a.configCredentials(config)
	}
	if s.path != "" {
		return a.loadSecretCredentials(ctx, s, flake)
	}
	if path := os.Getenv("CHALKOSCONFIG"); path != "" {
		return a.configCredentials(path)
	}
	if _, found, err := secretsPath(s, flake); err != nil {
		return nil, err
	} else if found {
		return a.loadSecretCredentials(ctx, s, flake)
	}
	path := defaultConfigPath(a.home)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("there is no secrets file in %s and no client file at %s; pass --secrets or --config", flake, path)
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

// clusterFor loads the cluster definition for commands a client file may run. Without one, when
// the credentials come from a client file, the nodes are those the file names.
func (a *app) clusterFor(ctx context.Context, f clusterFlags, creds *credentials) (*cluster, error) {
	if creds.config != nil && f.manifest == "" && f.cluster == "" {
		if _, err := os.Stat(filepath.Join(f.flake, "flake.nix")); errors.Is(err, fs.ErrNotExist) {
			return clusterOfConfig(*creds.config, f), nil
		}
	}
	c, err := a.loadCluster(ctx, f)
	if err != nil {
		return nil, err
	}
	if creds.config != nil && creds.config.Cluster != c.manifest.Cluster.Name {
		return nil, fmt.Errorf("%s is a client file of the cluster %s, not %s", creds.configPath, creds.config.Cluster, c.manifest.Cluster.Name)
	}
	return c, nil
}

// clusterOfConfig is the cluster as far as a client file knows it: its name and its nodes'
// addresses.
func clusterOfConfig(c client.Config, f clusterFlags) *cluster {
	m := &manifest.Manifest{Cluster: manifest.Cluster{Name: c.Cluster}, Nodes: map[string]manifest.Node{}}
	for name, addr := range c.Nodes {
		network := map[string]any{"networks": map[string]any{"client-file": map[string]any{"address": []any{addr}}}}
		m.Nodes[name] = manifest.Node{Identity: manifest.Identity{Network: network}}
	}
	return &cluster{flags: f, manifest: m, partial: true}
}

// configNew issues a client file from the secrets file: a certificate of the role for a new
// key, the OS CA, and the nodes' addresses.
func (a *app) configNew(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("config new", flag.ContinueOnError)
	var cf clusterFlags
	var sf secretFlags
	cf.register(fs)
	sf.register(fs)
	name := fs.String("name", "", "the user's name, which the certificate carries")
	role := fs.String("role", "", "the role the certificate grants: admin, operator or reader")
	ttl := fs.Duration("ttl", 8760*time.Hour, "validity of the certificate")
	out := fs.String("out", "", "file to write (default ~/.config/chalkos/config)")
	force := fs.Bool("force", false, "replace an existing file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *name == "" || *role == "" {
		return errors.New("usage: chalkctl config new --name NAME --role admin|operator|reader [--ttl 8760h] [--out FILE] [--force]")
	}
	c, err := a.loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	secrets, err := a.loadSecrets(ctx, sf, cf.flake)
	if err != nil {
		return err
	}
	nodes := map[string]string{}
	for node, n := range c.manifest.Nodes {
		if addrs := n.Identity.StaticAddresses(); len(addrs) > 0 {
			nodes[node] = addrs[0]
		}
	}
	config, err := client.NewConfig(secrets.ClientCA(), secrets.OSCABundle(), c.manifest.Cluster.Name, *name, *role, *ttl, nodes, time.Now())
	if err != nil {
		return err
	}
	data, err := config.Encode()
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = defaultConfigPath(a.home)
	}
	// The file holds the user's private key: a directory created for it is its owner's alone.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// The file holds the user's private key.
	if *force {
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
	fmt.Fprintf(a.stdout, "wrote %s for %s with the %s role; its certificate expires on %s\n", path, *name, *role, leaf.NotAfter.UTC().Format(time.DateOnly))
	return nil
}
