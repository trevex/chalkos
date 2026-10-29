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
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
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

// Paths are the files chalkd reads and writes; tests point them at temporary directories.
type Paths struct {
	// StateDir is where STATE is mounted.
	StateDir string
	// BootDisk and BootPartitions are udev's links to the boot disk and its partitions.
	BootDisk       string
	BootPartitions string
	OSRelease      string
	EFIVars        string
	// TPM lists the node's TPMs.
	TPM           string
	StorageStatus string
	MountInfo     string
	// ESP is where the ESP is mounted, and Cmdline the kernel's command line.
	ESP     string
	Cmdline string
	// FailedBoot, on VAR, keeps what chalkd and the health check logged during the last boot that
	// was not found healthy.
	FailedBoot string
	// Facts is where the image declares the facts its rules read.
	Facts string
	// Diagnostics is where the image declares its diagnostics.
	Diagnostics string
	// EffectsBaseline, below /run, is what the effects of a delivery that failed part way
	// compare with until a delivery is applied whole: the config, the facts and the outputs as they
	// were before the first delivery that failed.
	EffectsBaseline string
}

// DefaultPaths are the paths on a node.
func DefaultPaths() Paths {
	return Paths{
		StateDir:        "/state",
		BootDisk:        "/dev/disk/chalk-boot-disk",
		BootPartitions:  "/dev/disk/chalk-boot",
		OSRelease:       "/etc/os-release",
		EFIVars:         "/sys/firmware/efi/efivars",
		TPM:             "/sys/class/tpm",
		StorageStatus:   "/run/chalkos/storage-status.json",
		MountInfo:       "/proc/self/mountinfo",
		ESP:             "/efi",
		Cmdline:         "/proc/cmdline",
		FailedBoot:      "/var/lib/chalkd/failed-boot",
		Facts:           facts.DefaultPath,
		Diagnostics:     diagnostics.DefaultPath,
		EffectsBaseline: "/run/chalkd/effects-baseline.json",
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
	// InstallImage installs an upgrade's image with the config and the fallback secret the upgrade
	// delivers; nil installs it on the boot disk. Tests replace it.
	InstallImage func(ctx context.Context, h upgrade.Header, config, secret string, image io.Reader) (upgrade.Result, error)
	// ReadImage reads the store of an image in the inactive slot, as apply-config --staged checks
	// config against it; nil mounts it from the boot disk. Tests replace it.
	ReadImage func(ctx context.Context, rootHash []byte, read func(st imageStore, system string) error) error
	// ReactivateImage makes the image the inactive slot holds, by its root hash, boot next again,
	// as a rollback does, calling record as upgrade.Node.Rollback does; nil re-activates it on the
	// boot disk. Tests replace it.
	ReactivateImage func(ctx context.Context, rootHash []byte, record func() error) (upgrade.Result, error)
	// Healthy checks the node as the health check of a boot does, the image's layers included,
	// which ConfirmConfig waits for before it ends a config's trial. ignore names the units the
	// trial leaves out besides those the image ignores. nil finds the node healthy. Tests replace
	// it.
	Healthy func(ctx context.Context, ignore []string) error
	// Journal streams the journal of the boot, or of one unit.
	Journal func(ctx context.Context, unit string, follow bool) (io.ReadCloser, error)
	// RebootNode reboots the node; it is called once the response has been sent.
	RebootNode func()
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

	// clock is the time client certificates are verified at; nil is the system clock.
	clock func() time.Time
	// actedOnSelf, when set, is told the layer actions each delivery runs; tests set it.
	actedOnSelf func([]string)

	// mu serialises calls that change the node.
	mu        sync.Mutex
	installed bool
	// upgrading is held while an upgrade runs, which changes only the inactive slot and the ESP.
	upgrading sync.Mutex
	// trialTimer rolls the config on trial back at its deadline; nil without a trial. s.mu guards
	// it.
	trialTimer *time.Timer
	// trialArmed counts the deadlines armed, so a deadline that fires knows whether it is the one
	// armed last. s.mu guards it.
	trialArmed uint64
	// trialRetry is the time before a rollback at a trial's deadline that failed is tried again;
	// zero is a minute. Tests set it.
	trialRetry time.Duration
	// trialTimeout is the time a rollback at a trial's deadline may take; zero is
	// trialRollbackTimeout. Tests set it.
	trialTimeout time.Duration
	// trialCtx is the context the rollbacks of trials derive theirs from, which ends when chalkd
	// stops; nil is context.Background. ResumeTrial sets it, and s.mu guards it.
	trialCtx context.Context
	// logStreams counts the Logs calls running.
	logStreams atomic.Int32
	// diagnosticRuns counts the RunDiagnostic calls running.
	diagnosticRuns atomic.Int32
}

// permissions are the modes and least role of each procedure of the node API. The authorizer
// refuses a procedure without an entry.
var permissions = map[string]layer.Permission{
	nodev1connect.NodeServiceInfoProcedure:           {Modes: layer.AnyMode, Role: pki.RoleReader},
	nodev1connect.NodeServiceDisksProcedure:          {Modes: layer.AnyMode, Role: pki.RoleReader},
	nodev1connect.NodeServiceContractProcedure:       {Modes: layer.AnyMode, Role: pki.RoleReader},
	nodev1connect.NodeServiceInstallProcedure:        {Modes: layer.MaintenanceOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceApplyConfigProcedure:    {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceGetConfigProcedure:      {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceResetVolumeProcedure:    {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceRollbackConfigProcedure: {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceConfirmConfigProcedure:  {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	nodev1connect.NodeServiceStatusProcedure:         {Modes: layer.NormalOnly, Role: pki.RoleReader},
	nodev1connect.NodeServiceLogsProcedure:           {Modes: layer.AnyMode, Role: pki.RoleReader},
	nodev1connect.NodeServiceRebootProcedure:         {Modes: layer.AnyMode, Role: pki.RoleOperator},
	nodev1connect.NodeServiceDiagnosticsProcedure:    {Modes: layer.AnyMode, Role: pki.RoleReader},
	// RunDiagnostic also needs the diagnostic's own role, which it checks itself, as the table
	// cannot see the request.
	nodev1connect.NodeServiceRunDiagnosticProcedure: {Modes: layer.AnyMode, Role: pki.RoleReader},
	// Upgrade needs the admin role when its header carries config or a fallback secret, as
	// ApplyConfig does; Upgrade checks that itself, as the table cannot see the header.
	nodev1connect.NodeServiceUpgradeProcedure:       {Modes: layer.NormalOnly, Role: pki.RoleOperator},
	nodev1connect.NodeServiceRollbackImageProcedure: {Modes: layer.NormalOnly, Role: pki.RoleOperator},
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

// unverifiedKey holds why a client certificate no longer verifies.
type unverifiedKey struct{}

// Handler serves the node API and every compiled plugin's API. Roles come from the client
// certificate's chains, verified for every request against the OS CAs the node trusts then: see
// pki.ClientRole. The chains TLS verified when the connection was made would keep a client of a
// root the node stopped trusting, or one whose certificate expired, authorised for as long as it
// keeps the connection open.
func (s *Server) Handler() http.Handler {
	opts := []connect.HandlerOption{
		connect.WithInterceptors(authorizer{s: s, gates: s.gates()}),
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

// authorizer refuses calls of a layer the image lacks, in the wrong mode or with too little a
// role, in that order.
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
	if err, ok := ctx.Value(unverifiedKey{}).(error); ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("the client certificate does not verify against the OS CAs the node trusts: %w", err))
	}
	role, _ := ctx.Value(roleKey{}).(pki.Role)
	if !role.Allows(r.Role) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s needs the %s role", procedure, r.Role))
	}
	return nil
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

// storage is the node's storage setup, as the booted system sees it.
func (s *Server) storage() *node.Storage {
	return &node.Storage{
		Run:            s.Run,
		Host:           s.Host,
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
		return storage.Section{}, storage.Pins{}, failed(connect.CodeInternal, "read the recorded storage section: %v", err)
	}
	pins, err := storage.ReadPins(st.PinsFile())
	if err != nil {
		return storage.Section{}, storage.Pins{}, failed(connect.CodeInternal, "%v", err)
	}
	return section, pins, nil
}

func (s *Server) Reboot(ctx context.Context, _ *connect.Request[nodev1.RebootRequest]) (*connect.Response[nodev1.RebootResponse], error) {
	s.rebootSoon()
	return connect.NewResponse(&nodev1.RebootResponse{}), nil
}

// rebootSoon reboots once the response has had time to reach the client.
func (s *Server) rebootSoon() {
	go func() {
		time.Sleep(time.Second)
		s.RebootNode()
	}()
}

// name is the node's name: the subject of its node certificate, which chalkctl issues for the
// node's inventory name. chalkd keeps the name nowhere else.
//   - In normal mode chalkd always serves its node certificate; a node without one, or with one
//     that names no node, is broken, and no name stands in for it.
//   - An install takes the name from the certificate in its header instead.
func (s *Server) name() (string, error) {
	if s.Certificate == nil {
		return "", failed(connect.CodeInternal, "the node serves no node certificate, which names the node")
	}
	if leaf := s.Certificate.Current().Leaf; leaf != nil && leaf.Subject.CommonName != "" {
		return leaf.Subject.CommonName, nil
	}
	return "", failed(connect.CodeInternal, "the node's node certificate names no node")
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

func failed(code connect.Code, format string, args ...any) error {
	return connect.NewError(code, fmt.Errorf(format, args...))
}

// count is n and the noun, in the plural unless n is one.
func count[N ~int | ~uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
