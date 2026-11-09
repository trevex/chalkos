// Command chalkd is the chalkos node agent. It serves the node API on port 50000 and applies the
// node's config at boot.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkd: ")
	root, err := newCommand()
	if err != nil {
		log.Fatal(err)
	}
	os.Exit(chalkd.Execute(root))
}

// newCommand is chalkd's command tree: its own commands, and those of each compiled layer below
// the layer's name, as the units of the layer's own module run them. modules/node/runtime/effects.nix
// lists the names of chalkd's own commands, which an effect's run of chalkd may use without naming
// a layer.
func newCommand() (*cobra.Command, error) {
	node := chalkd.Offline(chalkd.DefaultPaths())
	reg, err := layer.NewRegistry(layers.Set{}, plugins(node)...)
	if err != nil {
		return nil, err
	}
	return chalkd.NewCommand(reg,
		&cobra.Command{
			Use:   "serve",
			Short: "Serve the node API",
			Args:  chalkd.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return serve() },
		},
		&cobra.Command{
			Use:   "load-config",
			Short: "Apply the config recorded on STATE",
			Long: "Apply the config recorded on STATE: its identity and documents, after ending a config's trial\n" +
				"that is still open.",
			Args: chalkd.NoArgs,
			RunE: func(*cobra.Command, []string) error { return loadConfig() },
		},
		healthCommand(health),
		checkCommand(),
	), nil
}

// checkCommand is chalkd check --system <system> --facts <socket>.
func checkCommand() *cobra.Command {
	var system, socket string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check the config on standard input as an image checks it",
		Long: "Check the config on standard input as the image whose NixOS system is --system checks it, with\n" +
			"the facts the socket --facts serves, and write the result to standard output.",
		Args: chalkd.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if system == "" || socket == "" {
				return chalkd.Usagef("%s takes --system and --facts", cmd.CommandPath())
			}
			return checkConfig(system, socket, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&system, "system", "", "NixOS system of the image whose check runs")
	cmd.Flags().StringVar(&socket, "facts", "", "socket that serves the facts of the node")
	_ = cmd.MarkFlagRequired("system")
	_ = cmd.MarkFlagRequired("facts")
	return cmd
}

// checkConfig is chalkd check: the image whose NixOS system is system checks the config on stdin
// for itself, as the chalkd of another image runs it in a sandbox before the image boots, and
// writes the result to stdout. Only the compiled layers are asked, for what the image's layer
// facts give config's translation: the sandbox's /etc is the running system's, not this image's.
func checkConfig(system, socket string, stdin io.Reader, stdout io.Writer) error {
	dir, err := os.MkdirTemp("", "chalkos-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	node := chalkd.Offline(chalkd.DefaultPaths())
	if node.Plugins, err = layer.NewRegistry(layers.Set{}, plugins(node)...); err != nil {
		return err
	}
	return node.Check(context.Background(), system, socket, dir, stdin, stdout)
}

// healthArgs are what chalkd health takes: how long to wait for the boot to become healthy, or
// whether systemd stopped such a wait, and the units whose failure does not count.
type healthArgs struct {
	wait    time.Duration
	stopped bool
	ignore  []string
}

// healthCommand is chalkd health [--wait SECONDS | --stopped] [--ignore-unit UNIT]..., which runs
// run with what it takes.
func healthCommand(run func(healthArgs) error) *cobra.Command {
	var a healthArgs
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check whether this boot is healthy",
		Long: "Check whether this boot is healthy. With --wait, check until it is, and reboot after an\n" +
			"unhealthy boot of an image the boot loader counts. With --stopped, after a wait ended before it\n" +
			"decided, do as it would have: record the boot, and reboot when that leads on. The failure of a\n" +
			"unit --ignore-unit names does not count.",
		Args: chalkd.NoArgs,
		RunE: func(*cobra.Command, []string) error { return run(a) },
	}
	cmd.Flags().Var((*seconds)(&a.wait), "wait", "check until the boot is healthy, for this many seconds at most")
	cmd.Flags().BoolVar(&a.stopped, "stopped", false, "end a wait that systemd stopped before it decided")
	cmd.Flags().StringArrayVar(&a.ignore, "ignore-unit", nil, "unit whose failure does not count; repeat it for more units")
	cmd.MarkFlagsMutuallyExclusive("wait", "stopped")
	return cmd
}

// seconds is the value of a flag that takes a positive number of seconds.
type seconds time.Duration

func (s *seconds) String() string { return strconv.Itoa(int(time.Duration(*s) / time.Second)) }
func (s *seconds) Type() string   { return "seconds" }

func (s *seconds) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fmt.Errorf("takes a number of seconds, not %q", v)
	}
	*s = seconds(time.Duration(n) * time.Second)
	return nil
}

// loadConfig applies the config recorded on STATE, with the image's layers writing what they
// derive from it. A config's trial that is still open ends first: the slot runs the config from
// before it again. A trial that cannot be ended is logged, and its record takes a deadline that
// passed, so chalkd rolls it back at once as it starts.
func loadConfig() error {
	// Only the layers' config is asked for, so the server they are bound to serves nothing.
	node := chalkd.Offline(chalkd.DefaultPaths())
	loader := nodeconfig.Default()
	if err := chalkd.RestoreTrialAtBoot(node.Paths.StateDir, loader.Slots); err != nil {
		log.Printf("end the open trial of a config: %v", err)
	}
	reg, err := registry(node)
	if err != nil {
		return err
	}
	if loader.Contract, err = contract.Load(contract.DefaultDir, contract.RenderRoot(loader.Render.Root)); err != nil {
		return err
	}
	loader.Layers = layer.Active[nodeconfig.Layer](reg)
	return loader.Load()
}

// health checks this boot once, or with a.wait until it is healthy or the time passed. With
// a.stopped it handles the end of a wait that did not decide, which systemd describes in
// $SERVICE_RESULT and $EXIT_STATUS.
func health(a healthArgs) error {
	// Only the layers' hooks are asked, so the server they are bound to serves nothing.
	node := chalkd.Offline(chalkd.DefaultPaths())
	reg, err := registry(node)
	if err != nil {
		return err
	}
	h := chalkd.DefaultHealth(layer.Active[layer.Health](reg))
	h.IgnoreUnits = a.ignore
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	switch {
	case a.stopped:
		return h.Stopped(ctx, os.Getenv("SERVICE_RESULT"), os.Getenv("EXIT_STATUS"))
	case a.wait > 0:
		return h.Wait(ctx, a.wait)
	}
	if err := h.Check(ctx); err != nil {
		return fmt.Errorf("not healthy: %w", err)
	}
	log.Print("healthy")
	return nil
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
	if bundle, err := os.ReadFile(imageCA); err == nil {
		pool, err := chalkd.OSCAPool(string(bundle))
		if err != nil {
			return credentials{}, fmt.Errorf("%s: %w", imageCA, err)
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
	if err := atomicfile.WriteFile(keyPath, []byte(self.Key), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := atomicfile.WriteFile(certPath, []byte(self.Certificate), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

// renewOnApplyConfig reports whether the renewal hook is on, which the chalklab test image
// alone sets; chalkd says so in its log, so a node that runs it by mistake shows it.
func renewOnApplyConfig(getenv func(string) string) bool {
	if getenv("CHALKD_TEST_RENEW_ON_APPLY_CONFIG") != "1" {
		return false
	}
	log.Print("test image: renewing the certificates after each ApplyConfig")
	return true
}

// healthIgnoreUnits are the units whose failure leaves the node healthy, as the image's health
// check ignores them, which chalkd's unit passes in CHALKD_HEALTH_IGNORE_UNITS separated by spaces.
func healthIgnoreUnits(getenv func(string) string) []string {
	return strings.Fields(getenv("CHALKD_HEALTH_IGNORE_UNITS"))
}

// healthTimeout is the time a counted boot of the image has to become healthy, which chalkd's
// unit passes in CHALKD_HEALTH_TIMEOUT in seconds, as the boot's health check waits; zero without
// it.
func healthTimeout(getenv func(string) string) (time.Duration, error) {
	v := getenv("CHALKD_HEALTH_TIMEOUT")
	if v == "" {
		return 0, nil
	}
	seconds, err := strconv.Atoi(v)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("CHALKD_HEALTH_TIMEOUT takes a number of seconds, not %q", v)
	}
	return time.Duration(seconds) * time.Second, nil
}

func serve() error {
	paths := chalkd.DefaultPaths()
	creds, err := loadCredentials(paths.StateDir, "/etc/chalkos/os-ca.crt", paths.RunDir, time.Now())
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
		Run:         tool.ExecRunner{},
		Host:        storage.DefaultHost(),
		Loader:      nodeconfig.Default(),
		InPlace:     install.ForNode(paths.StateDir, paths.RunDir).InPlace,
		FromParts:   install.ForInstaller(paths.RunDir).FromParts,
		Journal:     chalkd.Journal,
	}
	srv.RebootNode = func() {
		if _, err := tool.Run(context.Background(), srv.Run, "systemctl", "reboot"); err != nil {
			log.Printf("reboot: %v", err)
		}
	}

	// The running image's contract does not change while it runs.
	if srv.Loader.Contract, err = contract.Load(contract.DefaultDir, contract.RenderRoot(srv.Loader.Render.Root)); err != nil {
		return err
	}
	srv.RenewOnApplyConfig = renewOnApplyConfig(os.Getenv)
	if srv.Plugins, err = registry(srv); err != nil {
		return err
	}
	srv.Loader.Layers = layer.Active[nodeconfig.Layer](srv.Plugins)
	if creds.mode == nodev1.Mode_MODE_NORMAL {
		// A config's trial is confirmed only on a node the boot's health check would find
		// healthy, with the units it ignores.
		h := chalkd.DefaultHealth(layer.Active[layer.Health](srv.Plugins))
		h.IgnoreUnits = healthIgnoreUnits(os.Getenv)
		srv.Healthy = h.CheckIgnoring
		if srv.HealthTimeout, err = healthTimeout(os.Getenv); err != nil {
			return err
		}
	}

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
	// The active layers' loops run until chalkd stops. On every way out, a signal or a server that
	// failed, ctx ends and chalkd waits for them, so the node releases the VIPs first.
	var loops sync.WaitGroup
	defer func() {
		stop()
		loops.Wait()
	}()
	if creds.mode == nodev1.Mode_MODE_NORMAL {
		for _, p := range srv.Plugins.Active() {
			loops.Go(func() {
				if err := p.Start(ctx); err != nil {
					log.Printf("%s: %v", p.Name(), err)
				}
			})
		}
	}
	// The issuer of the image's layer renews the node certificate. Without one, the renewal only
	// logs once it is due, and Status names chalkctl node renew.
	if creds.mode == nodev1.Mode_MODE_NORMAL {
		var issue func(context.Context, []byte) (string, error)
		if is := srv.Plugins.Issuer(); is != nil {
			issue = is.IssueNodeCertificate
		}
		srv.Renewal = chalkd.NewNodeRenewal(creds.node, issue)
		go srv.Renewal.Run(ctx)
	}
	go announceAddresses(srv.CurrentFingerprint)

	ln, err := net.Listen("tcp", ":50000")
	if err != nil {
		return err
	}
	// A trial recorded before chalkd started gets its deadline again, and one the boot could not
	// end rolls back at once. Its delivery reads srv.Renewal and the layers, so it comes once
	// they are set and their loops run, and once chalkd listens, so the node can be reached while
	// it runs. It ends with ctx, and chalkd waits for it as for the loops.
	if creds.mode == nodev1.Mode_MODE_NORMAL {
		loops.Go(func() { srv.ResumeTrial(ctx) })
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
