// Command chalkd is the chalkos node agent. It serves the node API on port 50000 and applies the
// node's identity at boot.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

const usage = `usage: chalkd <command>

commands:
  serve               serve the node API
  load-identity       apply the identity recorded on STATE
  prepare-kubernetes  pick the node's address and write its Kubernetes certificates and configuration`

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkd: ")
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "load-identity":
		err = identity.Default().Load()
	case "prepare-kubernetes":
		err = knode.Prepare(knode.DefaultPaths(), time.Now(), knode.ResolveNodeIP)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// credentials are what chalkd serves with in its mode.
type credentials struct {
	mode nodev1.Mode
	cert tls.Certificate
	// clientCAs issue the client certificates chalkd requires; nil accepts any client.
	clientCAs *x509.CertPool
}

// loadCredentials picks the mode: an installed node serves its node certificate and requires
// clients of the OS CA on STATE; otherwise chalkd serves a new self-signed certificate and
// requires clients of the OS CA the image carries, if it carries one. A node chalkd cannot tell
// is installed or not fails closed rather than falling back to maintenance mode.
func loadCredentials(stateDir, imageCA, runDir string, now time.Time) (credentials, error) {
	installed, err := install.Installed(stateDir)
	if err != nil {
		return credentials{}, fmt.Errorf("tell whether the node is installed: %w", err)
	}
	if installed {
		dir := filepath.Join(stateDir, "chalkd")
		cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "node.crt"), filepath.Join(dir, "node.key"))
		if err != nil {
			return credentials{}, fmt.Errorf("load the node certificate: %w", err)
		}
		pool, err := loadPool(filepath.Join(dir, "ca.crt"))
		if err != nil {
			return credentials{}, err
		}
		return credentials{mode: nodev1.Mode_MODE_NORMAL, cert: cert, clientCAs: pool}, nil
	}

	cert, err := maintenanceCertificate(runDir, now)
	if err != nil {
		return credentials{}, err
	}
	c := credentials{mode: nodev1.Mode_MODE_MAINTENANCE, cert: cert}
	if _, err := os.Stat(imageCA); err == nil {
		if c.clientCAs, err = loadPool(imageCA); err != nil {
			return credentials{}, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return credentials{}, err
	}
	return c, nil
}

// maintenanceCertificate returns the self-signed certificate of maintenance mode. A restarted
// chalkd keeps the one it had, so the fingerprint an operator pinned stays valid; runDir is on
// tmpfs, so a reboot makes a new one. A pair that is missing, damaged or expired is replaced.
func maintenanceCertificate(runDir string, now time.Time) (tls.Certificate, error) {
	certPath := filepath.Join(runDir, "maintenance.crt")
	keyPath := filepath.Join(runDir, "maintenance.key")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.Chmod(runDir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && cert.Leaf != nil &&
		now.After(cert.Leaf.NotBefore) && now.Before(cert.Leaf.NotAfter) {
		return cert, nil
	}

	hostname, _ := os.Hostname()
	self, err := pki.SelfSigned(hostname, now)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair([]byte(self.Certificate), []byte(self.Key))
	if err != nil {
		return tls.Certificate{}, err
	}
	// A crash between the two writes leaves a pair that does not match, which the next start
	// replaces.
	if err := install.WriteFile(keyPath, []byte(self.Key), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := install.WriteFile(certPath, []byte(self.Certificate), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

func loadPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("%s holds no certificate", path)
	}
	return pool, nil
}

func serve() error {
	paths := chalkd.DefaultPaths()
	creds, err := loadCredentials(paths.StateDir, "/etc/chalkos/os-ca.crt", "/run/chalkd", time.Now())
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(creds.cert.Certificate[0])
	if err != nil {
		return err
	}
	srv := &chalkd.Server{
		Mode:        creds.mode,
		Installer:   os.Getenv("CHALKD_INSTALLER") == "1",
		AnyClient:   creds.clientCAs == nil,
		Fingerprint: pki.Fingerprint(leaf.Raw),
		Paths:       paths,
		Run:         node.ExecRunner{},
		Host:        storage.DefaultHost(),
		Identity:    identity.Default(),
		InPlace:     install.Default(true).InPlace,
		FromMedia:   install.Default(false).FromMedia,
		Journal:     chalkd.Journal,
		RebootNode: func() {
			if out, err := exec.Command("systemctl", "reboot").CombinedOutput(); err != nil {
				log.Printf("reboot: %v: %s", err, out)
			}
		},
	}

	switch {
	case creds.mode == nodev1.Mode_MODE_NORMAL:
		log.Printf("normal mode; certificate fingerprint %s", srv.Fingerprint)
	case srv.AnyClient:
		log.Printf("maintenance mode, accepting any client; certificate fingerprint %s", srv.Fingerprint)
	default:
		log.Printf("maintenance mode, accepting clients of the OS CA; certificate fingerprint %s", srv.Fingerprint)
	}
	// Images of a role with Kubernetes carry the cluster file.
	if _, err := os.Stat(knode.DefaultPaths().Cluster); err == nil && creds.mode == nodev1.Mode_MODE_NORMAL {
		srv.Kubernetes = chalkd.NewKubernetes()
		srv.Kubernetes.Start()
	}
	go announceAddresses(srv.Fingerprint)

	ln, err := net.Listen("tcp", ":50000")
	if err != nil {
		return err
	}
	return httpServer(srv.Handler(), chalkd.TLSConfig(creds.cert, creds.clientCAs)).ServeTLS(ln, "", "")
}

// httpServer bounds what a client can hold without sending requests: the time to send headers,
// their size, and idle connections. It sets no read or write timeout, which would cut off
// Install while it streams an image and Logs while it follows the journal.
func httpServer(h http.Handler, cfg *tls.Config) *http.Server {
	return &http.Server{
		Handler:           h,
		TLSConfig:         cfg,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
}

// announceAddresses prints the node's addresses, with the fingerprint, whenever they change, so
// an operator at the console can reach and verify the node.
func announceAddresses(fingerprint string) {
	var last string
	for {
		if addrs := addresses(); addrs != last {
			last = addrs
			log.Printf("addresses %s; certificate fingerprint %s", addrs, fingerprint)
		}
		time.Sleep(5 * time.Second)
	}
}

func addresses() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "unknown"
	}
	var ips []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			ips = append(ips, n.IP.String())
		}
	}
	slices.Sort(ips)
	if len(ips) == 0 {
		return "none yet"
	}
	return strings.Join(ips, " ")
}
