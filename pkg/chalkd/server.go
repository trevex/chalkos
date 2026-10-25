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
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
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
	// Journal streams the journal of the boot, or of one unit.
	Journal func(ctx context.Context, unit string, follow bool) (io.ReadCloser, error)
	// RebootNode reboots the node; it is called once the response has been sent.
	RebootNode func()
	// Facts runs the commands of facts; nil runs them with systemd-run. Tests replace it.
	Facts facts.Runner
	// Layers are the layers the image carries, as its markers name them.
	Layers layers.Set
	// Kubernetes is the node's Kubernetes side; nil in maintenance mode and on an image without
	// the Kubernetes layer.
	Kubernetes *Kubernetes
	// Renewal renews the node certificate; nil on a node that renews none.
	Renewal *Renewal
	// RenewOnApplyConfig renews the node certificate and the control plane's certificates after
	// each ApplyConfig that delivers config, due or not. Only test images set it, to renew within
	// a test's time.
	RenewOnApplyConfig bool
	// RevokedNodes returns the nodes revoked in the cluster, whose node certificates a control
	// plane does not renew; nil reads them through the node's own API server. Tests replace it.
	RevokedNodes func(context.Context) (revoked.List, error)

	// clock is the time client certificates are verified at; nil is the system clock.
	clock func() time.Time
	// actedOnSelf, when set, is told what each delivery asked of chalkd's own loops; tests set it.
	actedOnSelf func(effects.ChalkdAction)

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
	role  pki.Role
	// layer is the image layer the RPC belongs to; layers.Base for those of every image.
	layer string
}

// The modes an RPC may be called in.
var (
	anyMode         = []nodev1.Mode{maintenance, normal}
	maintenanceOnly = []nodev1.Mode{maintenance}
	normalOnly      = []nodev1.Mode{normal}
)

// permissions are the modes, least role and layer of each RPC. The authorizer refuses an RPC
// without an entry, and one of a layer the image lacks as unimplemented, so the handlers of a
// layer's RPCs run only on images with it.
var permissions = map[string]permission{
	nodev1connect.NodeServiceInfoProcedure:                 {anyMode, pki.RoleReader, layers.Base},
	nodev1connect.NodeServiceDisksProcedure:                {anyMode, pki.RoleReader, layers.Base},
	nodev1connect.NodeServiceContractProcedure:             {anyMode, pki.RoleReader, layers.Base},
	nodev1connect.NodeServiceInstallProcedure:              {maintenanceOnly, pki.RoleAdmin, layers.Base},
	nodev1connect.NodeServiceApplyConfigProcedure:          {normalOnly, pki.RoleAdmin, layers.Base},
	nodev1connect.NodeServiceGetConfigProcedure:            {normalOnly, pki.RoleAdmin, layers.Base},
	nodev1connect.NodeServiceResetVolumeProcedure:          {normalOnly, pki.RoleAdmin, layers.Base},
	nodev1connect.NodeServiceStatusProcedure:               {normalOnly, pki.RoleReader, layers.Base},
	nodev1connect.NodeServiceLogsProcedure:                 {anyMode, pki.RoleReader, layers.Base},
	nodev1connect.NodeServiceRebootProcedure:               {anyMode, pki.RoleOperator, layers.Base},
	nodev1connect.NodeServiceBootstrapProcedure:            {normalOnly, pki.RoleAdmin, layers.Kubernetes},
	nodev1connect.NodeServiceEtcdMembersProcedure:          {normalOnly, pki.RoleReader, layers.Kubernetes},
	nodev1connect.NodeServiceEtcdRemoveMemberProcedure:     {normalOnly, pki.RoleAdmin, layers.Kubernetes},
	nodev1connect.NodeServiceEtcdLeaveProcedure:            {normalOnly, pki.RoleAdmin, layers.Kubernetes},
	nodev1connect.NodeServiceRenewNodeCertificateProcedure: {normalOnly, pki.RoleNode, layers.Base},
	nodev1connect.NodeServiceRotationStepProcedure:         {normalOnly, pki.RoleAdmin, layers.Kubernetes},
	nodev1connect.NodeServiceDrainNodeProcedure:            {normalOnly, pki.RoleOperator, layers.Kubernetes},
	nodev1connect.NodeServiceUncordonNodeProcedure:         {normalOnly, pki.RoleOperator, layers.Kubernetes},
	nodev1connect.NodeServiceRevokeNodeProcedure:           {normalOnly, pki.RoleAdmin, layers.Kubernetes},
	nodev1connect.NodeServiceUnrevokeNodeProcedure:         {normalOnly, pki.RoleAdmin, layers.Kubernetes},
	// Upgrade needs the admin role when its header carries config or a fallback secret, as
	// ApplyConfig does; Upgrade checks that itself, as the table cannot see the header.
	nodev1connect.NodeServiceUpgradeProcedure: {normalOnly, pki.RoleOperator, layers.Base},
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

// authorizer refuses calls of a layer the image lacks, in the wrong mode or with too little a
// role, in that order.
type authorizer struct{ s *Server }

func (a authorizer) check(ctx context.Context, procedure string) error {
	r, ok := permissions[procedure]
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s is not served", procedure))
	}
	if !a.s.Layers.Has(r.layer) {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("the %s layer is not in this image", r.layer))
	}
	if !slices.Contains(r.modes, a.s.Mode) {
		if a.s.Mode == maintenance {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is in maintenance mode; install it first"))
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is installed"))
	}
	if err, ok := ctx.Value(unverifiedKey{}).(error); ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("the client certificate does not verify against the OS CAs the node trusts: %w", err))
	}
	role, _ := ctx.Value(roleKey{}).(pki.Role)
	if !role.Allows(r.role) {
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
