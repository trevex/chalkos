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
	// Fingerprint is the SHA-256 of the certificate chalkd serves.
	Fingerprint string
	Paths       Paths
	Run         node.Runner
	Host        storage.Host
	Identity    identity.Loader
	// InPlace and FromMedia install the node; tests replace them.
	InPlace   func(context.Context, install.Request) error
	FromMedia func(context.Context, install.MediaRequest) error
	// Journal streams the journal of the boot, or of one unit.
	Journal func(ctx context.Context, unit string, follow bool) (io.ReadCloser, error)
	// RebootNode reboots the node; it is called once the response has been sent.
	RebootNode func()

	// mu serialises calls that change the node.
	mu        sync.Mutex
	installed bool
	// logStreams counts the Logs calls running.
	logStreams atomic.Int32
}

// permission is where an RPC may be called and the least role it needs.
type permission struct {
	modes []nodev1.Mode
	role  string
}

var permissions = map[string]permission{
	nodev1connect.NodeServiceInfoProcedure:          {[]nodev1.Mode{maintenance, normal}, pki.RoleReader},
	nodev1connect.NodeServiceDisksProcedure:         {[]nodev1.Mode{maintenance, normal}, pki.RoleReader},
	nodev1connect.NodeServiceInstallProcedure:       {[]nodev1.Mode{maintenance}, pki.RoleAdmin},
	nodev1connect.NodeServiceApplyIdentityProcedure: {[]nodev1.Mode{normal}, pki.RoleAdmin},
	nodev1connect.NodeServiceResetVolumeProcedure:   {[]nodev1.Mode{normal}, pki.RoleAdmin},
	nodev1connect.NodeServiceStatusProcedure:        {[]nodev1.Mode{normal}, pki.RoleReader},
	nodev1connect.NodeServiceLogsProcedure:          {[]nodev1.Mode{maintenance, normal}, pki.RoleReader},
	nodev1connect.NodeServiceRebootProcedure:        {[]nodev1.Mode{maintenance, normal}, pki.RoleOperator},
}

type roleKey struct{}

// Handler serves the API. Roles come from the verified client certificate's Organization.
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
		switch {
		case r.TLS != nil && len(r.TLS.VerifiedChains) > 0:
			role, _ = pki.Role(r.TLS.VerifiedChains[0][0])
		case s.AnyClient:
			role = pki.RoleAdmin
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), roleKey{}, role)))
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

// TLSConfig serves cert and, with clientCAs, requires client certificates they issued.
func TLSConfig(cert tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	if clientCAs != nil {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.ClientCAs = clientCAs
	}
	return cfg
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
