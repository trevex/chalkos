// Package chalkd is the node agent's API server. In maintenance mode, before the node is
// installed, it serves a self-signed certificate and installs the node; in normal mode it serves
// the node certificate, requires client certificates from the OS CA, and applies identities.
package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/chalkd/renewal"
	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/imagecheck"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/upgrade"
)

const (
	maintenance = nodev1.Mode_MODE_MAINTENANCE
	normal      = nodev1.Mode_MODE_NORMAL
)

const (
	// maxMessageBytes limits each request message, as received and after decompression. The
	// largest is an install message: a header carries the identity or config, certificates,
	// repart definitions and, from the installer, the target image's schemas, under 1 MiB
	// together; an image chunk from chalkctl is 1 MiB.
	maxMessageBytes = 4 << 20
	// maxLogStreams limits concurrent Logs calls, each of which holds a journalctl process
	// until the client goes away.
	maxLogStreams = 8
	// maxDiagnosticRuns limits concurrent RunDiagnostic calls, each of which holds a transient
	// unit until its command ends.
	maxDiagnosticRuns = 4
)

// Paths are the files chalkd reads and writes; tests take those PathsUnder a temporary directory.
type Paths struct {
	// StateDir is where STATE is mounted.
	StateDir string
	// BootDisk and BootPartitions are udev's links to the boot disk and its partitions.
	BootDisk       string
	BootPartitions string
	OSRelease      string
	EFIVars        string
	// TPM lists the node's TPMs, and EventLog is the event log of the firmware's measurements into
	// the TPM.
	TPM           string
	EventLog      string
	StorageStatus string
	MountInfo     string
	// ESP is where the ESP is mounted, Cmdline the kernel's command line, and BootID the ID the
	// kernel gives the boot.
	ESP     string
	Cmdline string
	BootID  string
	// FailedBoot, on VAR, keeps what chalkd and the health check logged during the last boot that
	// was not found healthy.
	FailedBoot string
	// RunDir is chalkd's directory on /run, where it mounts what it checks and installs.
	RunDir string
	// EffectsBaseline, in RunDir, is what the effects of a delivery that failed part way
	// compare with until a delivery is applied whole: the config, the facts and the outputs as they
	// were before the first delivery that failed.
	EffectsBaseline string
}

// DefaultPaths are the paths on a node.
func DefaultPaths() Paths { return PathsUnder("/") }

// PathsUnder are the paths of a node whose file system is below root. BootDisk and BootPartitions
// name devices, which storage.Host resolves below its own roots, so they are a node's under any
// root.
func PathsUnder(root string) Paths {
	at := func(path string) string { return filepath.Join(root, path) }
	runDir := at("/run/chalkd")
	return Paths{
		StateDir:        at(storage.StateDir),
		BootDisk:        "/dev/disk/chalk-boot-disk",
		BootPartitions:  "/dev/disk/chalk-boot",
		OSRelease:       at("/etc/os-release"),
		EFIVars:         at("/sys/firmware/efi/efivars"),
		TPM:             at("/sys/class/tpm"),
		EventLog:        at(secureboot.EventLog),
		StorageStatus:   at("/run/chalkos/storage-status.json"),
		MountInfo:       at("/proc/self/mountinfo"),
		ESP:             at("/efi"),
		Cmdline:         at("/proc/cmdline"),
		BootID:          at("/proc/sys/kernel/random/boot_id"),
		FailedBoot:      at("/var/lib/chalkd/failed-boot"),
		RunDir:          runDir,
		EffectsBaseline: filepath.Join(runDir, "effects-baseline.json"),
	}
}

// Server implements the node API.
type Server struct {
	Mode nodev1.Mode
	// Installer is set on the installer image, which installs nodes onto other disks.
	Installer bool
	// AnyClient accepts clients without certificates, as admins. Only maintenance mode on an
	// image without an OS CA does.
	AnyClient bool
	// Fingerprint is the SHA-256 of the certificate chalkd served when it started, in either
	// mode; CurrentFingerprint follows renewals of the node certificate.
	Fingerprint string
	// ClientCAs returns the OS CAs every call is verified by, as TLSConfig is given; nil uses
	// those of Certificate.
	ClientCAs func() *x509.CertPool
	// Certificate is the node certificate chalkd serves in normal mode.
	Certificate *NodeCertificate
	Paths       Paths
	Run         tool.Runner
	Host        storage.Host
	// Loader applies the node's stored config, as chalkos-config.service does at boot.
	Loader nodeconfig.Loader
	// InPlace and FromParts install the node; tests replace them.
	InPlace   func(ctx context.Context, req install.Request, rootHash []byte) error
	FromParts func(context.Context, install.PartsRequest) error
	// RemoveState deletes the STATE a node in maintenance mode booted with from its own disk, so
	// its next boot creates STATE again; tests replace it.
	RemoveState func(ctx context.Context) error
	// WriteEFIVariable writes the attributes and payload of an EFI variable to its file in
	// efivarfs, path; nil writes through efivarfs. Tests replace it.
	WriteEFIVariable func(path string, attrs uint32, payload []byte) error
	// CheckImage runs the checker of an image that does not run; nil runs it in the sandbox of
	// imagecheck. Tests run it in their own process.
	CheckImage imagecheck.Runner
	// Healthy checks the node as the health check of a boot does, the image's layers included,
	// which ConfirmConfig waits for before it ends a config's trial and Status reports of the boot
	// when asked. ignore names the units the trial leaves out besides those the image ignores. nil
	// finds the node healthy. Tests replace it.
	Healthy func(ctx context.Context, ignore []string) error
	// HealthTimeout is the time a counted boot of the image has to become healthy. Status reports
	// a boot settled once it passed after systemd finished starting the boot.
	HealthTimeout time.Duration
	// Monotonic reads CLOCK_MONOTONIC, which systemd's timestamps of the boot count in; nil reads
	// the kernel's. Tests replace it.
	Monotonic func() (time.Duration, error)
	// Journal streams the journal of the boot, or of one unit.
	Journal func(ctx context.Context, unit string, follow bool) (io.ReadCloser, error)
	// RebootNode reboots the node; it is called once the response has been sent.
	RebootNode func()
	// PowerOffNode powers the node off; it is called once the response has been sent.
	PowerOffNode func()
	// RebootDelay is how long the node waits after a response before it reboots or powers off, so
	// the response reaches the client; zero or less means a second. Tests shorten it.
	RebootDelay time.Duration
	// Facts runs the commands of facts; nil runs them with systemd-run. Tests replace it.
	Facts facts.Runner
	// Diagnose runs the diagnostics' commands; nil runs them as transient systemd units. Tests
	// replace it.
	Diagnose diagnostics.Runner
	// Plugins are the layers compiled into chalkd and those of them the image carries; nil
	// serves the node API alone, as on an image of the base alone.
	Plugins *layer.Registry
	// Renewal renews the node certificate in normal mode, through the image's issuer when it has
	// one; nil in maintenance mode.
	Renewal *renewal.Renewal
	// RenewOnApplyConfig renews the node certificate and the layers' certificates after each
	// ApplyConfig that delivers config, due or not. Only test images set it, to renew within a
	// test's time.
	RenewOnApplyConfig bool
	// ConsoleActions lets the console reboot and shut down the node and roll back its image and
	// config, as the image's chalkos.console.actions.enable says.
	ConsoleActions bool

	// clock is the time client certificates are verified at; nil is the system clock.
	clock func() time.Time
	// actedOnSelf, when set, is told the layer actions each delivery runs; tests set it.
	actedOnSelf func([]string)
	// slots are the image slots upgrades, activations and image rollbacks change; nil is the boot
	// disk's. Tests supply their own.
	slots imageSlots

	// mu serialises calls that change the node.
	mu        sync.Mutex
	installed bool
	// upgrading is held while an upgrade runs, which changes only the inactive slot and the ESP.
	upgrading sync.Mutex
	// trial rolls the config on trial back at its deadline; s.mu guards it.
	trial trialDeadline
	// logStreams counts the Logs calls running.
	logStreams atomic.Int32
	// diagnosticRuns counts the RunDiagnostic calls running.
	diagnosticRuns atomic.Int32
}

// Offline is the Server of chalkd's commands that do not serve, such as check, health and the
// layers' own: their plugins are bound to it, and it runs commands on the node and reads its files
// at paths, but serves nothing.
func Offline(paths Paths) *Server {
	return &Server{Paths: paths, Run: tool.ExecRunner{}}
}

// permissions are the modes, least role and console access of each procedure of the node API. The
// authorizer refuses a procedure without an entry.
var permissions = map[string]layer.Permission{
	nodev1connect.NodeServiceInfoProcedure:              {Modes: layer.AnyMode, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
	nodev1connect.NodeServiceDisksProcedure:             {Modes: layer.AnyMode, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
	nodev1connect.NodeServiceContractProcedure:          {Modes: layer.AnyMode, Role: pki.RoleReader},
	nodev1connect.NodeServiceInstallProcedure:           {Modes: layer.MaintenanceOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceEnrollSecureBootProcedure:  {Modes: layer.MaintenanceOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceSecureBootDriversProcedure: {Modes: layer.MaintenanceOnly, Role: pki.RoleReader},
	nodev1connect.NodeServiceApplyConfigProcedure:       {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceGetConfigProcedure:         {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceResetVolumeProcedure:       {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceRollbackConfigProcedure:    {Modes: layer.NormalOnly, Role: pki.RoleAdmin, Console: layer.ConsoleAction},
	nodev1connect.NodeServiceConfirmConfigProcedure:     {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceStatusProcedure:            {Modes: layer.NormalOnly, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
	nodev1connect.NodeServiceLogsProcedure:              {Modes: layer.AnyMode, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
	nodev1connect.NodeServiceRebootProcedure:            {Modes: layer.AnyMode, Role: pki.RoleOperator, Console: layer.ConsoleAction},
	nodev1connect.NodeServiceShutdownProcedure:          {Modes: layer.AnyMode, Role: pki.RoleOperator, Console: layer.ConsoleAction},
	nodev1connect.NodeServiceDiagnosticsProcedure:       {Modes: layer.AnyMode, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
	// RunDiagnostic also needs the diagnostic's own role, which it checks itself, as the table
	// cannot see the request.
	nodev1connect.NodeServiceRunDiagnosticProcedure: {Modes: layer.AnyMode, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
	// Upgrade needs the admin role when its header carries config or a fallback secret, as
	// ApplyConfig does; Upgrade checks that itself, as the table cannot see the header.
	nodev1connect.NodeServiceUpgradeProcedure:       {Modes: layer.NormalOnly, Role: pki.RoleOperator},
	nodev1connect.NodeServiceActivateImageProcedure: {Modes: layer.NormalOnly, Role: pki.RoleOperator},
	nodev1connect.NodeServiceRollbackImageProcedure: {Modes: layer.NormalOnly, Role: pki.RoleOperator, Console: layer.ConsoleAction},
}

// gate is where a procedure may be called: its permission and its layer, layers.Base for the
// node API.
type gate struct {
	layer.Permission
	layer string
}

// gates are the permissions of the node API and of every compiled plugin's API. The authorizer
// refuses a procedure of a layer the image lacks as unimplemented, so the handlers of a layer's
// procedures run only on images with it.
func (s *Server) gates() map[string]gate {
	g := map[string]gate{}
	for procedure, p := range permissions {
		g[procedure] = gate{p, layers.Base}
	}
	for _, plugin := range s.Plugins.Compiled() {
		for procedure, p := range plugin.Permissions() {
			g[procedure] = gate{p, plugin.Name()}
		}
	}
	return g
}

type roleKey struct{}

// consoleKey marks a call on the console's socket, which has no role: each procedure's
// Permission.Console decides it instead.
type consoleKey struct{}

// onConsole reports whether the call came through the console's socket.
func onConsole(ctx context.Context) bool {
	console, _ := ctx.Value(consoleKey{}).(bool)
	return console
}

// unverifiedKey holds why a client certificate no longer verifies.
type unverifiedKey struct{}

// Handler serves the node API and every compiled plugin's API. Roles come from the client
// certificate's chains, verified for every request against the OS CAs the node trusts then: see
// pki.ClientRole. The chains TLS verified when the connection was made would keep a client of a
// root the node stopped trusting, or one whose certificate expired, authorised for as long as it
// keeps the connection open.
func (s *Server) Handler() http.Handler {
	mux := s.mux()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var role pki.Role
		ctx := r.Context()
		switch {
		case r.TLS != nil && len(r.TLS.PeerCertificates) > 0:
			chains, err := s.verifyClient(r.TLS.PeerCertificates)
			if err != nil {
				// The certificate will not verify again on this connection; the client is to
				// make a new one, whose handshake the current OS CAs decide.
				w.Header().Set("Connection", "close")
				ctx = context.WithValue(ctx, unverifiedKey{}, err)
				break
			}
			role, _ = pki.ClientRole(chains)
			ctx = layer.WithPeer(ctx, chains[0][0])
		case s.AnyClient:
			role = pki.RoleAdmin
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(ctx, roleKey{}, role)))
	})
}

// ConsoleHandler serves the node API and every compiled plugin's API to the console, on chalkd's
// local socket: its calls have no role, and each procedure's Permission.Console grants or refuses
// them.
func (s *Server) ConsoleHandler() http.Handler {
	mux := s.mux()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), consoleKey{}, true)))
	})
}

// mux serves the node API and every compiled plugin's API behind the authorizer.
func (s *Server) mux() *http.ServeMux {
	opts := []connect.HandlerOption{
		connect.WithInterceptors(authorizer{s: s, gates: s.gates()}, errorCodes{}),
		connect.WithReadMaxBytes(maxMessageBytes),
		// An oversized compressed request would otherwise be decompressed in full before being
		// discarded; refusing compression up front keeps that cost off chalkd.
		connect.WithCompression("gzip", nil, nil),
	}
	mux := http.NewServeMux()
	mux.Handle(nodev1connect.NewNodeServiceHandler(s, opts...))
	for _, p := range s.Plugins.Compiled() {
		mux.Handle(p.Handler(opts...))
	}
	return mux
}

// verifyClient verifies a client's certificate, with the certificates it presented after it as
// intermediates, against the OS CAs the node trusts now.
func (s *Server) verifyClient(presented []*x509.Certificate) ([][]*x509.Certificate, error) {
	var roots *x509.CertPool
	switch {
	case s.ClientCAs != nil:
		roots = s.ClientCAs()
	case s.Certificate != nil:
		roots = s.Certificate.ClientCAs()
	}
	if roots == nil {
		return nil, errors.New("the node trusts no OS CA")
	}
	intermediates := x509.NewCertPool()
	for _, cert := range presented[1:] {
		intermediates.AddCert(cert)
	}
	now := time.Now()
	if s.clock != nil {
		now = s.clock()
	}
	return presented[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

// authorizer refuses calls of a layer the image lacks, in the wrong mode, and with too little a
// role or, on the console's socket, what the console may not call, in that order.
type authorizer struct {
	s     *Server
	gates map[string]gate
}

func (a authorizer) check(ctx context.Context, procedure string) error {
	r, ok := a.gates[procedure]
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s is not served", procedure))
	}
	if !a.s.Plugins.Layers().Has(r.layer) {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("the %s layer is not in this image", r.layer))
	}
	if !slices.Contains(r.Modes, a.s.Mode) {
		if a.s.Mode == maintenance {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is in maintenance mode; install it first"))
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is installed"))
	}
	if onConsole(ctx) {
		return a.checkConsole(procedure, r.Console)
	}
	if err, ok := ctx.Value(unverifiedKey{}).(error); ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("the client certificate does not verify against the OS CAs the node trusts: %w", err))
	}
	role, _ := ctx.Value(roleKey{}).(pki.Role)
	if !role.Allows(r.Role) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s needs the %s role", procedure, r.Role))
	}
	return nil
}

// checkConsole refuses the console a procedure its permission does not grant it, and its actions
// while the image turns them off.
func (a authorizer) checkConsole(procedure string, c layer.Console) error {
	switch {
	case c == layer.ConsoleAllowed, c == layer.ConsoleAction && a.s.ConsoleActions:
		return nil
	case c == layer.ConsoleAction:
		return connect.NewError(connect.CodePermissionDenied, errors.New("the image turns the console's actions off"))
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("the console may not call %s", procedure))
}

func (a authorizer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := a.check(ctx, req.Spec().Procedure); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a authorizer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a authorizer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := a.check(ctx, conn.Spec().Procedure); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// domainCodes are the codes of the domain errors chalkd's handlers return without one of their
// own: the refusals of an image or of its activation fail the precondition, those of config by an
// image's check as refusalCode says, and OS CAs the node refuses to trust are the client's to fix.
var domainCodes = layer.Codes{
	layer.CodeFor[*upgrade.RefusalError](connect.CodeFailedPrecondition),
	layer.CodeFor[*upgrade.CheckError](connect.CodeFailedPrecondition),
	layer.CodeFor[*upgrade.StagedRetiredError](connect.CodeFailedPrecondition),
	layer.CodeFor[refusedTrust](connect.CodeInvalidArgument),
	refusalCode,
}

// errorCodes turns the error of a handler, chalkd's or a plugin's, into the *connect.Error it
// answers with: its whole text, with the code it carries or the code of a domain error it wraps,
// or else CodeInternal. Handlers decide a code with layer.Errorf and wrap everything else with
// fmt.Errorf, which keeps the error chain.
type errorCodes struct{}

func (errorCodes) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		if err != nil {
			return nil, domainCodes.Error(err)
		}
		return resp, nil
	}
}

func (errorCodes) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (errorCodes) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := next(ctx, conn); err != nil {
			return domainCodes.Error(err)
		}
		return nil
	}
}

// TLSConfig serves the certificate getCertificate returns for each connection and, with
// clientCAs, requires client certificates that verify against the pool it returns for each
// connection, so the OS CAs a node trusts change without a restart.
func TLSConfig(getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error), clientCAs func() *x509.CertPool) *tls.Config {
	cfg := &tls.Config{GetCertificate: getCertificate, MinVersion: tls.VersionTLS13}
	if clientCAs != nil {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
			c := cfg.Clone()
			c.GetConfigForClient = nil
			c.ClientCAs = clientCAs()
			return c, nil
		}
	}
	return cfg
}

// StaticCAs verifies clients by one pool, as chalkd does in maintenance mode.
func StaticCAs(pool *x509.CertPool) func() *x509.CertPool {
	if pool == nil {
		return nil
	}
	return func() *x509.CertPool { return pool }
}

// hostOps mounts and opens volumes on the booted system with s.Run, against chalkd's mount table
// and the host's /dev.
func (s *Server) hostOps() hostops.Tools {
	return hostops.Tools{Run: s.Run, MountInfo: s.Paths.MountInfo, DevRoot: s.Host.DevRoot}
}

// storage is the node's storage setup, as the booted system sees it.
func (s *Server) storage() *node.Storage {
	return &node.Storage{
		Run:            s.Run,
		Host:           s.Host,
		Ops:            s.hostOps(),
		StateDir:       s.Paths.StateDir,
		BootDisk:       s.Paths.BootDisk,
		BootPartitions: s.Paths.BootPartitions,
		StatusFile:     s.Paths.StorageStatus,
	}
}

// recorded reads the storage section and pins STATE holds.
func (s *Server) recorded() (storage.Section, storage.Pins, error) {
	st := s.storage()
	section, err := storage.ReadSection(st.SectionFile())
	if err != nil {
		return storage.Section{}, storage.Pins{}, fmt.Errorf("read the recorded storage section: %w", err)
	}
	pins, err := storage.ReadPins(st.PinsFile())
	if err != nil {
		return storage.Section{}, storage.Pins{}, err
	}
	return section, pins, nil
}

func (s *Server) Reboot(ctx context.Context, _ *connect.Request[nodev1.RebootRequest]) (*connect.Response[nodev1.RebootResponse], error) {
	s.rebootSoon()
	return connect.NewResponse(&nodev1.RebootResponse{}), nil
}

func (s *Server) Shutdown(ctx context.Context, _ *connect.Request[nodev1.ShutdownRequest]) (*connect.Response[nodev1.ShutdownResponse], error) {
	s.afterResponse(s.PowerOffNode)
	return connect.NewResponse(&nodev1.ShutdownResponse{}), nil
}

// rebootSoon reboots once the response has had time to reach the client.
func (s *Server) rebootSoon() { s.afterResponse(s.RebootNode) }

// afterResponse calls f once the response has had time to reach the client.
func (s *Server) afterResponse(f func()) {
	delay := time.Second
	if s.RebootDelay > 0 {
		delay = s.RebootDelay
	}
	go func() {
		time.Sleep(delay)
		f()
	}()
}

// name is the node's name: the subject of its node certificate, which chalkctl issues for the
// node's inventory name. chalkd keeps the name nowhere else.
//   - In normal mode chalkd always serves its node certificate; a node without one, or with one
//     that names no node, is broken, and no name stands in for it.
//   - An install takes the name from the certificate in its header instead.
func (s *Server) name() (string, error) {
	if s.Certificate == nil {
		return "", errors.New("the node serves no node certificate, which names the node")
	}
	if leaf := s.Certificate.Current().Leaf; leaf != nil && leaf.Subject.CommonName != "" {
		return leaf.Subject.CommonName, nil
	}
	return "", errors.New("the node's node certificate names no node")
}

// nodeName is the node's name for the commands its messages advise, as name has it. A node
// without one says <node>, which chalkctl replaces with the name it was called with.
func (s *Server) nodeName() string {
	if name, err := s.name(); err == nil {
		return name
	}
	return "<node>"
}

// Server is the layer.Node its plugins are given.
var _ layer.Node = (*Server)(nil)

func (s *Server) NodeName() string { return s.nodeName() }

func (s *Server) NodeCertificate() layer.Certificate {
	if s.Certificate == nil {
		return nil
	}
	return s.Certificate
}

func (s *Server) Changes() sync.Locker { return &s.mu }

func (s *Server) Runner() tool.Runner { return s.Run }

func (s *Server) Slot() (string, error) { return s.Loader.Slot() }

// count is n and the noun, in the plural unless n is one.
func count[N ~int | ~uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
