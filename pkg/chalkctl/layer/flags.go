package layer

import (
	"errors"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/inventory"
)

// ClusterFlags name the cluster a command operates.
type ClusterFlags struct {
	// Inventory is --inventory; empty for the default file, when it exists.
	Inventory string
}

// Register registers --inventory.
func (f *ClusterFlags) Register(fs *pflag.FlagSet) {
	fs.StringVar(&f.Inventory, "inventory", "", "inventory naming the cluster's nodes, their addresses and their config files (default "+inventory.DefaultFile+" in the current directory, when it exists)")
}

// SecretsFlags say where the secrets file is and how to decrypt it.
type SecretsFlags struct {
	// Path is --secrets, - for standard input.
	Path string
	// Identities are --identity, the files of age identities that decrypt the secrets file.
	Identities []string
}

// Register registers --secrets and --identity.
func (f *SecretsFlags) Register(fs *pflag.FlagSet) {
	fs.StringVar(&f.Path, "secrets", "", "secrets file, encrypted with age or not, or - to read it from standard input")
	fs.StringArrayVar(&f.Identities, "identity", nil, "file holding an age identity to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)")
}

// NodeFlags are the flags of a command that reaches a cluster's nodes.
type NodeFlags struct {
	ClusterFlags
	Secrets SecretsFlags
	// Address is --address.
	Address Addresses
	// Config is --config, the client file of a command that a client file may run.
	Config string
}

// Register registers --inventory, the secrets file's flags and --address, of a command that
// needs the secrets file.
func (f *NodeFlags) Register(fs *pflag.FlagSet) {
	f.ClusterFlags.Register(fs)
	f.Secrets.Register(fs)
	fs.Var(&f.Address, "address", "address of a node's chalkd: host or host:port for the node the command names, or NODE=ADDR for any node; may be repeated (default each node's address in the inventory)")
}

// RegisterClient registers the flags of a command that a client file may run: those of Register
// and --config.
func (f *NodeFlags) RegisterClient(fs *pflag.FlagSet) {
	f.Register(fs)
	fs.StringVar(&f.Config, "config", "", "client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)")
}

// Addresses are the values of --address, which may be repeated: ADDR, host or host:port, is the
// address of the one node the command reaches, and NODE=ADDR that of any node of the cluster. No
// host or host:port holds "=". Of two values for one node, the later counts.
type Addresses struct{ values []string }

func (a *Addresses) String() string { return strings.Join(a.values, ",") }

// Type names the value for pflag, which help shows.
func (a *Addresses) Type() string { return "[NODE=]ADDR" }

func (a *Addresses) Set(v string) error {
	node, addr, named := strings.Cut(v, "=")
	if !named {
		addr = v
	}
	if addr == "" || named && node == "" {
		return errors.New("want ADDR or NODE=ADDR")
	}
	a.values = append(a.values, v)
	return nil
}

// Bare is the last ADDR given without a node; empty when there is none.
func (a Addresses) Bare() string {
	for _, v := range slices.Backward(a.values) {
		if !strings.Contains(v, "=") {
			return v
		}
	}
	return ""
}

// Named reports whether a NODE=ADDR is among the values.
func (a Addresses) Named() bool {
	return slices.ContainsFunc(a.values, func(v string) bool { return strings.Contains(v, "=") })
}

// Resolve gives the addresses by node. A bare ADDR is the address of one, the node the command
// reaches; one is empty for a command that reaches several nodes, which refuses a bare ADDR.
func (a Addresses) Resolve(one string) (map[string]string, error) {
	addresses := map[string]string{}
	for _, v := range a.values {
		node, addr, named := strings.Cut(v, "=")
		if !named {
			if one == "" {
				return nil, cli.Usagef("--address %q: with several nodes, give each node's address as NODE=ADDR", v)
			}
			node, addr = one, v
		}
		addresses[node] = addr
	}
	return addresses, nil
}
