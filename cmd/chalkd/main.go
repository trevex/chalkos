// Command chalkd is the chalkos node agent. It serves the node API on port 50000 and applies the
// node's identity at boot.
package main

import (
	"context"
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
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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
  serve                            serve the node API
  load-identity                    apply the identity recorded on STATE
  health [--wait SECONDS]          check whether this boot is healthy; with --wait, check until
                                   it is, and reboot after an unhealthy boot of an image the
                                   boot loader counts
  prepare-kubernetes [vxlan-rule]  pick the node's addresses and write its Kubernetes
                                   certificates and configuration, then run vxlan-rule with the
                                   file saying where VXLAN may arrive`

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkd: ")
	if len(os.Args) < 2 || !validArgs(os.Args[1], os.Args[2:]) {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "load-identity":
		err = identity.Default().Load()
	case "health":
		err = health(os.Args[2:])
	case "prepare-kubernetes":
		var firewall knode.Firewall
		if len(os.Args) == 3 {
			firewall = knode.VXLANRule(os.Args[2])
		}
		err = knode.Prepare(knode.DefaultPaths(), time.Now(), knode.WaitForAddresses, firewall)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// validArgs reports whether a command has the arguments it takes.
func validArgs(command string, args []string) bool {
	switch command {
	case "prepare-kubernetes":
		return len(args) <= 1
	case "health":
		return len(args) == 0 || len(args) == 2 && args[0] == "--wait"
	}
	return len(args) == 0
}

// health checks this boot once, or with --wait until it is healthy or the seconds passed.
func health(args []string) error {
	var k *chalkd.Kubernetes
	if _, err := os.Stat(knode.DefaultPaths().Cluster); err == nil {
		k = chalkd.NewKubernetes()
	}
	h := chalkd.DefaultHealth(k)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if len(args) == 0 {
		if err := h.Check(ctx); err != nil {
			return fmt.Errorf("not healthy: %w", err)
		}
		log.Print("healthy")
		return nil
	}
	seconds, err := strconv.Atoi(args[1])
	if err != nil || seconds <= 0 {
		return fmt.Errorf("--wait takes a number of seconds, not %q", args[1])
	}
	return h.Wait(ctx, time.Duration(seconds)*time.Second)
}

// credentials are what chalkd serves with in its mode.
type credentials struct {
	mode nodev1.Mode
	// node is the node certificate of normal mode, maintenance the self-signed certificate of
	// maintenance mode.
	node        *chalkd.NodeCertificate
	maintenance *tls.Certificate
	// clientCAs returns the CAs that issue the client certificates chalkd requires; nil accepts
	// any client.
	clientCAs func() *x509.CertPool
}

// getCertificate serves the certificate of the mode.
func (c credentials) getCertificate() func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c.node != nil {
		return c.node.GetCertificate
	}
	return chalkd.StaticCertificate(c.maintenance)
}

// fingerprint is the SHA-256 of the certificate served now.
func (c credentials) fingerprint() (string, error) {
	if c.node != nil {
		return c.node.Fingerprint(), nil
	}
	leaf, err := x509.ParseCertificate(c.maintenance.Certificate[0])
	if err != nil {
		return "", err
	}
	return pki.Fingerprint(leaf.Raw), nil
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
		node, err := chalkd.LoadNodeCertificate(dir)
		if err != nil {
			return credentials{}, err
		}
		return credentials{mode: nodev1.Mode_MODE_NORMAL, node: node, clientCAs: node.ClientCAs}, nil
	}

	cert, err := maintenanceCertificate(runDir, now)
	if err != nil {
		return credentials{}, err
	}
	c := credentials{mode: nodev1.Mode_MODE_MAINTENANCE, maintenance: &cert}
	if _, err := os.Stat(imageCA); err == nil {
		pool, err := loadPool(imageCA)
		if err != nil {
			return credentials{}, err
		}
		c.clientCAs = chalkd.StaticCAs(pool)
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

// renewOnApplyIdentity reports whether the renewal hook is on, which the chalklab test image
// alone sets; chalkd says so in its log, so a node that runs it by mistake shows it.
func renewOnApplyIdentity(getenv func(string) string) bool {
	if getenv("CHALKD_TEST_RENEW_ON_APPLY_IDENTITY") != "1" {
		return false
	}
	log.Print("test image: renewing the certificates after each ApplyIdentity")
	return true
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
	fingerprint, err := creds.fingerprint()
	if err != nil {
		return err
	}
	srv := &chalkd.Server{
		Mode:        creds.mode,
		Installer:   os.Getenv("CHALKD_INSTALLER") == "1",
		AnyClient:   creds.clientCAs == nil,
		ClientCAs:   creds.clientCAs,
		Fingerprint: fingerprint,
		Certificate: creds.node,
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

	srv.RenewOnApplyIdentity = renewOnApplyIdentity(os.Getenv)

	switch {
	case creds.mode == nodev1.Mode_MODE_NORMAL:
		log.Printf("normal mode; certificate fingerprint %s", srv.Fingerprint)
	case srv.AnyClient:
		log.Printf("maintenance mode, accepting any client; certificate fingerprint %s", srv.Fingerprint)
	default:
		log.Printf("maintenance mode, accepting clients of the OS CA; certificate fingerprint %s", srv.Fingerprint)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	// Images of a role with Kubernetes carry the cluster file.
	if _, err := os.Stat(knode.DefaultPaths().Cluster); err == nil && creds.mode == nodev1.Mode_MODE_NORMAL {
		srv.Kubernetes = chalkd.NewKubernetes()
		srv.Kubernetes.Start()
		// On every way out the node releases the VIPs, which must not stay on a node whose chalkd
		// no longer holds their lease, and closes its etcd clients.
		defer srv.Kubernetes.Stop()
		// The cluster's control planes renew node certificates; a node without Kubernetes knows
		// none and is renewed with chalkctl node renew.
		srv.Renewal = chalkd.NewNodeRenewal(creds.node, srv.IssueNodeCertificate)
		go srv.Renewal.Run(ctx)
	}
	go announceAddresses(srv.CurrentFingerprint)

	ln, err := net.Listen("tcp", ":50000")
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() {
		served <- httpServer(srv.Handler(), chalkd.TLSConfig(creds.getCertificate(), creds.clientCAs)).ServeTLS(ln, "", "")
	}()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	return nil
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

// announceAddresses prints the node's addresses with the fingerprint whenever either changes, so
// an operator at the console can reach and verify the node.
func announceAddresses(fingerprint func() string) {
	var last string
	for {
		if line := "addresses " + addresses() + "; certificate fingerprint " + fingerprint(); line != last {
			last = line
			log.Print(line)
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
