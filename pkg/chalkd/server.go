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
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/upgrade"
)

const (
	maintenance = nodev1.Mode_MODE_MAINTENANCE
	normal      = nodev1.Mode_MODE_NORMAL
)

const (
	// maxMessageBytes limits each request message, as received and after decompression. The
	// largest is an install message: a header carries the identity, certificates and repart
	// definitions, a few KiB each, and an image chunk from chalkctl is 1 MiB.
	maxMessageBytes = 4 << 20
	// maxLogStreams limits concurrent Logs calls, each of which holds a journalctl process
	// until the client goes away.
	maxLogStreams = 8
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
}

// DefaultPaths are the paths on a node.
func DefaultPaths() Paths {
	return Paths{
		StateDir:       "/state",
		BootDisk:       "/dev/disk/chalk-boot-disk",
		BootPartitions: "/dev/disk/chalk-boot",
		OSRelease:      "/etc/os-release",
		EFIVars:        "/sys/firmware/efi/efivars",
		TPM:            "/sys/class/tpm",
		StorageStatus:  "/run/chalkos/storage-status.json",
		MountInfo:      "/proc/self/mountinfo",
		ESP:            "/efi",
		Cmdline:        "/proc/cmdline",
		FailedBoot:     "/var/lib/chalkd/failed-boot",
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
	Run         node.Runner
	Host        storage.Host
	Identity    identity.Loader
	// InPlace and FromMedia install the node; tests replace them.
	InPlace   func(context.Context, install.Request) error
	FromMedia func(context.Context, install.MediaRequest) error
	// InstallImage installs an upgrade's image; nil installs it on the boot disk. Tests replace it.
	InstallImage func(context.Context, upgrade.Header, io.Reader) (upgrade.Result, error)
	// Journal streams the journal of the boot, or of one unit.
	Journal func(ctx context.Context, unit string, follow bool) (io.ReadCloser, error)
	// RebootNode reboots the node; it is called once the response has been sent.
	RebootNode func()
	// Kubernetes is the node's Kubernetes side; nil on a node of a role without Kubernetes.
	Kubernetes *Kubernetes
	// Renewal renews the node certificate; nil on a node that renews none.
	Renewal *Renewal
	// RenewOnApplyIdentity renews the node certificate and the control plane's certificates after
	// each ApplyIdentity that delivers an identity, due or not. Only test images set it, to renew
	// within a test's time.
	RenewOnApplyIdentity bool

	// clock is the time client certificates are verified at; nil is the system clock.
	clock func() time.Time

	// mu serialises calls that change the node.
	mu        sync.Mutex
	installed bool
	// upgrading is held while an upgrade runs, which changes only the inactive slot and the ESP.
	upgrading sync.Mutex
	// logStreams counts the Logs calls running.
	logStreams atomic.Int32
}

// permission is where an RPC may be called and the least role it needs.
type permission struct {
	modes []nodev1.Mode
	role  string
}

var permissions = map[string]permission{
	nodev1connect.NodeServiceInfoProcedure:                 {[]nodev1.Mode{maintenance, normal}, pki.RoleReader},
	nodev1connect.NodeServiceDisksProcedure:                {[]nodev1.Mode{maintenance, normal}, pki.RoleReader},
	nodev1connect.NodeServiceInstallProcedure:              {[]nodev1.Mode{maintenance}, pki.RoleAdmin},
	nodev1connect.NodeServiceApplyIdentityProcedure:        {[]nodev1.Mode{normal}, pki.RoleAdmin},
	nodev1connect.NodeServiceResetVolumeProcedure:          {[]nodev1.Mode{normal}, pki.RoleAdmin},
	nodev1connect.NodeServiceStatusProcedure:               {[]nodev1.Mode{normal}, pki.RoleReader},
	nodev1connect.NodeServiceLogsProcedure:                 {[]nodev1.Mode{maintenance, normal}, pki.RoleReader},
	nodev1connect.NodeServiceRebootProcedure:               {[]nodev1.Mode{maintenance, normal}, pki.RoleOperator},
	nodev1connect.NodeServiceBootstrapProcedure:            {[]nodev1.Mode{normal}, pki.RoleAdmin},
	nodev1connect.NodeServiceEtcdMembersProcedure:          {[]nodev1.Mode{normal}, pki.RoleReader},
	nodev1connect.NodeServiceEtcdRemoveMemberProcedure:     {[]nodev1.Mode{normal}, pki.RoleAdmin},
	nodev1connect.NodeServiceEtcdLeaveProcedure:            {[]nodev1.Mode{normal}, pki.RoleAdmin},
	nodev1connect.NodeServiceRenewNodeCertificateProcedure: {[]nodev1.Mode{normal}, pki.RoleNode},
	nodev1connect.NodeServiceRotationStepProcedure:         {[]nodev1.Mode{normal}, pki.RoleAdmin},
	nodev1connect.NodeServiceUpgradeProcedure:              {[]nodev1.Mode{normal}, pki.RoleOperator},
}

type roleKey struct{}

// peerKey holds the verified client certificate.
type peerKey struct{}

// unverifiedKey holds why a client certificate no longer verifies.
type unverifiedKey struct{}

// Handler serves the API. Roles come from the client certificate's chains, verified for every
// request against the OS CAs the node trusts then: see pki.ClientRole. The chains TLS verified
// when the connection was made would keep a client of a root the node stopped trusting, or one
// whose certificate expired, authorised for as long as it keeps the connection open.
func (s *Server) Handler() http.Handler {
	_, h := nodev1connect.NewNodeServiceHandler(s,
		connect.WithInterceptors(authorizer{s}),
		connect.WithReadMaxBytes(maxMessageBytes),
		// An oversized compressed request would otherwise be decompressed in full before being
		// discarded; refusing compression up front keeps that cost off chalkd.
		connect.WithCompression("gzip", nil, nil),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := ""
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
			ctx = context.WithValue(ctx, peerKey{}, chains[0][0])
		case s.AnyClient:
			role = pki.RoleAdmin
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(ctx, roleKey{}, role)))
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

// authorizer refuses calls in the wrong mode or with too little a role.
type authorizer struct{ s *Server }

func (a authorizer) check(ctx context.Context, procedure string) error {
	r, ok := permissions[procedure]
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s is not served", procedure))
	}
	allowed := false
	for _, m := range r.modes {
		allowed = allowed || m == a.s.Mode
	}
	if !allowed {
		if a.s.Mode == maintenance {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is in maintenance mode; install it first"))
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is installed"))
	}
	if err, ok := ctx.Value(unverifiedKey{}).(error); ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("the client certificate does not verify against the OS CAs the node trusts: %w", err))
	}
	role, _ := ctx.Value(roleKey{}).(string)
	if !pki.Allows(role, r.role) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s needs the %s role", procedure, r.role))
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
	dir := s.storage().StorageDir()
	section, err := storage.ReadSection(filepath.Join(dir, "storage.json"))
	if err != nil {
		return storage.Section{}, storage.Pins{}, failed(connect.CodeInternal, "read the recorded storage section: %v", err)
	}
	pins, err := storage.ReadPins(filepath.Join(dir, "disks.json"))
	if err != nil {
		return storage.Section{}, storage.Pins{}, failed(connect.CodeInternal, "%v", err)
	}
	return section, pins, nil
}

// rebootSoon reboots once the response has had time to reach the client.
func (s *Server) rebootSoon() {
	go func() {
		time.Sleep(time.Second)
		s.RebootNode()
	}()
}

func failed(code connect.Code, format string, args ...any) error {
	return connect.NewError(code, fmt.Errorf(format, args...))
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
