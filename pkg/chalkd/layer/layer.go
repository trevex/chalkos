// Package layer is what chalkd's layers plug in with. A layer is a Plugin compiled into chalkd:
// it serves its own API beside the node API and hooks into the common node agent through the
// optional interfaces below, which chalkd finds by type assertion. chalkd mounts every compiled
// plugin's API, and starts and asks for hooks only the plugins whose layers the image carries.
package layer

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/tool"
)

// Plugin is a layer of chalkd.
type Plugin interface {
	// Name is the layer, as its marker /etc/chalkos/layers/<name> names it.
	Name() string
	// Handler serves the layer's API with the options given: its path and handler, as connect's
	// generated New…Handler returns them.
	Handler(opts ...connect.HandlerOption) (string, http.Handler)
	// Permissions are the modes and least role of each of the layer's procedures.
	Permissions() map[string]Permission
	// Start runs the layer's background loops until ctx ends and returns once they stopped. chalkd
	// starts it in normal mode alone.
	Start(ctx context.Context) error
}

// Permission is where a procedure may be called and the least role it needs.
type Permission struct {
	Modes []nodev1.Mode
	Role  pki.Role
}

// The modes a procedure may be called in.
var (
	AnyMode         = []nodev1.Mode{nodev1.Mode_MODE_MAINTENANCE, nodev1.Mode_MODE_NORMAL}
	MaintenanceOnly = []nodev1.Mode{nodev1.Mode_MODE_MAINTENANCE}
	NormalOnly      = []nodev1.Mode{nodev1.Mode_MODE_NORMAL}
)

// Node is what the common chalkd gives its plugins.
type Node interface {
	// NodeName is the node's name for the commands messages advise: the subject of its node
	// certificate, or <node>, which chalkctl replaces with the name it was called with.
	NodeName() string
	// NodeCertificate is the certificate chalkd serves; nil while it serves none, as in
	// maintenance mode.
	NodeCertificate() Certificate
	// Changes is held by the calls that change the node, as ApplyConfig holds it.
	Changes() sync.Locker
	// Runner runs the node's programs.
	Runner() tool.Runner
	// Slot returns the running image's config slot on STATE.
	Slot() (string, error)
	// StateDir is where STATE is mounted.
	StateDir() string
}

// Certificate is the node certificate chalkd serves, as plugins use it.
type Certificate interface {
	// Current is the certificate and key served now.
	Current() *pki.NodeCredential
	// OSCA is the OS CAs the node trusts (PEM).
	OSCA() string
	// GetClientCertificate presents the certificate to a peer, as tls.Config takes it.
	GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error)
}

// Health is a plugin whose layer decides whether a boot is healthy once it runs.
type Health interface {
	// Health checks the layer's services. checked is false while the layer runs nothing to check
	// yet, such as a layer whose services have not started; the common checks decide then.
	Health(ctx context.Context) (checked bool, err error)
}

// Certificates is a plugin whose layer holds certificates and trusts CAs of its own.
type Certificates interface {
	// Certificates are the layer's certificates and what it trusts, as Status reports them.
	Certificates(ctx context.Context, now time.Time) ([]*nodev1.CertificateStatus, []*nodev1.TrustStatus)
}

// Delivery is a plugin whose layer takes payloads with config deliveries, checks the identity's
// part of its own and writes what the config gives it.
type Delivery interface {
	// Accepts reports whether the payload is one of the layer's.
	Accepts(payload *anypb.Any) bool
	// CheckIdentity checks the layer's part of an identity, as config translates to it, before a
	// slot keeps it.
	CheckIdentity(identity []byte) error
	// Plan checks a delivery before the node changes and returns what applying it does.
	Plan(ctx context.Context, d Planned) (Commit, error)
	// WriteConfig writes what the layer derives from the running slot's documents, nil for a
	// slot without documents, as the config loader does at boot and after each delivery.
	WriteConfig(docs config.Documents) error
}

// Issuer is a plugin whose layer issues the node's own certificate when it renews. An image has
// one issuer at most.
type Issuer interface {
	// IssueNodeCertificate returns a node certificate for the key of the PKCS #10 request,
	// followed by the certificate of the node CA that issued it (PEM).
	IssueNodeCertificate(ctx context.Context, request []byte) (string, error)
}

// ImageFacts is a plugin whose layer's images carry facts config is translated with.
type ImageFacts interface {
	// RunningFacts are the running image's facts; nil for an image without them.
	RunningFacts() (*anypb.Any, error)
	// SystemFacts reads the facts of an image that does not run, as an upgrade wrote its store:
	// read takes a path within the image's system.
	SystemFacts(read func(path string) ([]byte, error)) (*anypb.Any, error)
	// ContractFacts reads the facts of an image's contract: read takes a file name within the
	// image's directory, as its contract layer holds it.
	ContractFacts(read func(name string) ([]byte, error)) (*anypb.Any, error)
	// Target sets on t what the facts give config's translation; claimed is false for facts of
	// another layer.
	Target(facts *anypb.Any, t *config.Target) (claimed bool, err error)
}

// Commands is a plugin that adds chalkd subcommands, run as chalkd <layer> <name>.
type Commands interface {
	Commands() map[string]Command
}

// Command is a subcommand of a layer.
type Command struct {
	// Args are the command's arguments as chalkd's usage shows them, such as "[FILE]".
	Args string
	// Help says what the command does; chalkd's usage indents its lines.
	Help string
	// Run runs the command with the arguments after its name. It fails with a UsageError when it
	// does not take them.
	Run func(args []string) error
}

// UsageError is what a command fails with when it does not take its arguments.
type UsageError struct{ Err error }

func (e UsageError) Error() string { return e.Err.Error() }
func (e UsageError) Unwrap() error { return e.Err }

// NoArgs is a command that takes no arguments.
func NoArgs(run func() error) func(args []string) error {
	return func(args []string) error {
		if len(args) > 0 {
			return UsageError{fmt.Errorf("takes no argument %q", args[0])}
		}
		return run()
	}
}

// Actions is a plugin with actions on what only the running chalkd holds, which effects name as
// <layer>:<action>.
type Actions interface {
	Actions() map[string]func(context.Context) error
}
