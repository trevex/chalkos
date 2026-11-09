// Package layer is what chalkd's layers plug in with. A layer is a Plugin compiled into chalkd:
// it serves its own API beside the node API and hooks into the common node agent through the
// optional interfaces below, which chalkd finds by type assertion. chalkd mounts every compiled
// plugin's API and starts only the active plugins, those whose layers the image carries. It asks
// the active plugins for most hooks, and every compiled plugin for a command and for the hooks
// that concern an image other than the running one; each hook says which plugins chalkd asks.
//
// One hook is declared where it is called: a plugin whose layer writes what it derives from the
// running slot's documents implements nodeconfig.Layer, which the config loader asks of the
// active plugins at boot and after each delivery.
package layer

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
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
	// generated New…Handler returns them. chalkd mounts that of every compiled plugin, so that it
	// answers for a layer the image lacks.
	Handler(opts ...connect.HandlerOption) (string, http.Handler)
	// Permissions are the modes and least role of each of the layer's procedures; chalkd asks every
	// compiled plugin.
	Permissions() map[string]Permission
	// Start runs the layer's background loops until ctx ends and returns once they stopped. chalkd
	// starts the active plugins, in normal mode alone.
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

// Health is a plugin whose layer decides whether a boot is healthy once it runs. chalkd asks the
// active plugins.
type Health interface {
	// Health checks the layer's services. checked is false while the layer runs nothing to check
	// yet, such as a layer whose services have not started; the common checks decide then.
	Health(ctx context.Context) (checked bool, err error)
}

// Certificates is a plugin whose layer holds certificates and trusts CAs of its own. chalkd asks
// the active plugins.
type Certificates interface {
	// Certificates are the layer's certificates and what it trusts, as Status reports them.
	Certificates(ctx context.Context, now time.Time) ([]*nodev1.CertificateStatus, []*nodev1.TrustStatus)
}

// Delivery is a plugin whose layer takes payloads with config deliveries and checks the
// identity's part of its own. What a layer writes from the config is the hook nodeconfig.Layer.
type Delivery interface {
	// Accepts reports whether the payload is one of the layer's. chalkd asks the plugins it has
	// Plan.
	Accepts(payload *anypb.Any) bool
	// CheckIdentity checks the layer's part of an identity, as config translates to it, before a
	// slot keeps it. chalkd asks every compiled plugin: the identity may be for another image, as
	// an upgrade's slot is, so the check passes without the layer's part.
	CheckIdentity(identity []byte) error
	// Plan checks a delivery before the node changes and returns what applying it does; it changes
	// nothing itself, since a dry run calls it too and drops the Commit it returns. chalkd asks the
	// active plugins, and the installer, whose image carries no layer, every compiled plugin.
	Plan(ctx context.Context, d Planned) (Commit, error)
}

// Issuer is a plugin whose layer issues the node's own certificate when it renews. An image has
// one issuer at most, among the active plugins.
type Issuer interface {
	// IssueNodeCertificate returns a node certificate for the key of the PKCS #10 request,
	// followed by the certificate of the node CA that issued it (PEM).
	IssueNodeCertificate(ctx context.Context, request []byte) (string, error)
}

// ImageFacts is a plugin whose layer's images carry facts config is translated with.
type ImageFacts interface {
	// RunningFacts are the running image's facts; nil for an image without them. chalkd asks the
	// active plugins.
	RunningFacts() (*anypb.Any, error)
	// SystemFacts reads an image's facts from its NixOS system: read takes a path within that
	// system. chalkd check asks every compiled plugin, for the image it checks config for, whose
	// system is not the one the sandbox runs.
	SystemFacts(read func(path string) ([]byte, error)) (*anypb.Any, error)
	// ContractFacts reads the facts of an image's contract: read takes a file name within the
	// image's directory, as its contract layer holds it. chalkd asks every compiled plugin.
	ContractFacts(read func(name string) ([]byte, error)) (*anypb.Any, error)
	// Target adds to t's translators the layer's translation of an image with the facts; claimed
	// is false for facts of another layer. chalkd asks every compiled plugin, as the facts may be
	// another image's.
	Target(facts *anypb.Any, t *config.Target) (claimed bool, err error)
}

// Commands is a plugin that adds chalkd subcommands, run as chalkd <layer> <name>. chalkd adds
// those of every compiled plugin below a command named after the layer.
type Commands interface {
	Commands() []*cobra.Command
}

// Actions is a plugin with actions on what only the running chalkd holds, which effects name as
// <layer>:<action>. chalkd runs those of the active plugins.
type Actions interface {
	Actions() map[string]func(context.Context) error
}
