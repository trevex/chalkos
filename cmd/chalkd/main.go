// Command chalkd is the chalkos node agent. It serves the node API on port 50000 and applies the
// node's config at boot.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
)

// commonUsage is the usage of chalkd's own commands, which each layer's follow.
const commonUsage = `usage: chalkd <command>

commands:
  serve                            serve the node API
  load-config                      apply the config recorded on STATE: its identity and documents,
                                   after ending a config's trial that is still open
  health [--wait SECONDS] [--ignore-unit UNIT]...
                                   check whether this boot is healthy; with --wait, check until
                                   it is, and reboot after an unhealthy boot of an image the
                                   boot loader counts; failed units ignored do not count
  health --stopped                 after a wait ended before it decided, as it would have:
                                   record the boot and reboot when that leads on`

// commands are chalkd's own commands by name. Each takes the arguments after its name and fails
// with a layer.UsageError when it does not take them. modules/node/runtime/effects.nix lists their
// names, which an effect's run of chalkd may use without naming a layer.
var commands = map[string]func(args []string) error{
	"serve":       layer.NoArgs(serve),
	"load-config": layer.NoArgs(loadConfig),
	"health":      health,
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkd: ")
	// Subcommands need no server: the plugins are bound to one that serves nothing.
	node := &chalkd.Server{Paths: chalkd.DefaultPaths(), Run: tool.ExecRunner{}}
	reg, err := layer.NewRegistry(layers.Set{}, plugins(node)...)
	if err != nil {
		log.Fatal(err)
	}
	name, run, args := lookup(reg, os.Args[1:])
	if run == nil {
		fmt.Fprintln(os.Stderr, usage(reg))
		os.Exit(2)
	}
	err = run(args)
	if _, ok := errors.AsType[layer.UsageError](err); ok {
		fmt.Fprintf(os.Stderr, "chalkd %s: %v\n", name, err)
		fmt.Fprintln(os.Stderr, usage(reg))
		os.Exit(2)
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// lookup finds the command args name: one of chalkd's, or a layer's as <layer> <command>, which
// any compiled layer serves, as the units of the layer's own module run it. It returns the
// command's name, its function and its arguments; a nil function for no command.
func lookup(reg *layer.Registry, args []string) (string, func([]string) error, []string) {
	if len(args) == 0 {
		return "", nil, nil
	}
	if run := commands[args[0]]; run != nil {
		return args[0], run, args[1:]
	}
	if len(args) >= 2 {
		if cmd, ok := reg.Command(args[0], args[1]); ok {
			return args[0] + " " + args[1], cmd.Run, args[2:]
		}
	}
	return "", nil, nil
}

// usage is chalkd's usage: its own commands, then each layer's.
func usage(reg *layer.Registry) string {
	var b strings.Builder
	b.WriteString(commonUsage)
	for _, p := range reg.Compiled() {
		c, ok := p.(layer.Commands)
		if !ok {
			continue
		}
		cmds := c.Commands()
		for _, name := range slices.Sorted(maps.Keys(cmds)) {
			cmd := cmds[name]
			line := strings.TrimSpace(p.Name() + " " + name + " " + cmd.Args)
			help := strings.Split(cmd.Help, "\n")
			if len(line) < 33 {
				fmt.Fprintf(&b, "\n  %-32s %s", line, help[0])
			} else {
				fmt.Fprintf(&b, "\n  %s\n  %-32s %s", line, "", help[0])
			}
			for _, more := range help[1:] {
				fmt.Fprintf(&b, "\n  %-32s %s", "", more)
			}
		}
	}
	return b.String()
}

// healthArgs are what chalkd health takes: how long to wait for the boot to become healthy, or
// whether systemd stopped such a wait, and the units whose failure does not count.
type healthArgs struct {
	wait    time.Duration
	stopped bool
	ignore  []string
}

func parseHealthArgs(args []string) (healthArgs, error) {
	var a healthArgs
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wait := fs.String("wait", "", "")
	fs.BoolVar(&a.stopped, "stopped", false, "")
	fs.Func("ignore-unit", "", func(unit string) error {
		a.ignore = append(a.ignore, unit)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return healthArgs{}, err
	}
	if fs.NArg() > 0 {
		return healthArgs{}, fmt.Errorf("health takes no argument %q", fs.Arg(0))
	}
	if *wait != "" {
		seconds, err := strconv.Atoi(*wait)
		if err != nil || seconds <= 0 {
			return healthArgs{}, fmt.Errorf("--wait takes a number of seconds, not %q", *wait)
		}
		a.wait = time.Duration(seconds) * time.Second
	}
	if a.stopped && a.wait > 0 {
		return healthArgs{}, errors.New("--stopped and --wait exclude each other")
	}
	return a, nil
}

// loadConfig applies the config recorded on STATE, with the image's layers writing what they
// derive from it. A config's trial that is still open ends first: the slot runs the config from
// before it again. A trial that cannot be ended is logged, and chalkd settles it as it starts.
func loadConfig() error {
	// Only the layers' config is asked for, so the server they are bound to serves nothing.
	node := &chalkd.Server{Paths: chalkd.DefaultPaths(), Run: tool.ExecRunner{}}
	reg, err := registry(node)
	if err != nil {
		return err
	}
	loader := nodeconfig.Default()
	loader.Layers = layer.Active[nodeconfig.Layer](reg)
	if err := chalkd.RestoreTrialAtBoot(node.Paths.StateDir, loader.Slots); err != nil {
		log.Printf("end the open trial of a config: %v", err)
	}
	return loader.Load()
}

// health checks this boot once, or with --wait until it is healthy or the seconds passed. With
// --stopped it handles the end of a wait that did not decide, which systemd describes in
// $SERVICE_RESULT and $EXIT_STATUS.
func health(args []string) error {
	a, err := parseHealthArgs(args)
	if err != nil {
		return layer.UsageError{Err: err}
	}
	// Only the layers' hooks are asked, so the server they are bound to serves nothing.
	node := &chalkd.Server{Paths: chalkd.DefaultPaths(), Run: tool.ExecRunner{}}
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
		Run:         tool.ExecRunner{},
		Host:        storage.DefaultHost(),
		Loader:      nodeconfig.Default(),
		InPlace:     install.ForNode().InPlace,
		FromParts:   install.ForInstaller().FromParts,
		Journal:     chalkd.Journal,
		RebootNode: func() {
			if out, err := exec.Command("systemctl", "reboot").CombinedOutput(); err != nil {
				log.Printf("reboot: %v: %s", err, out)
			}
		},
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
		// A trial recorded before chalkd started gets its deadline again.
		srv.ResumeTrial()
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
