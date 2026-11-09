package layer

import (
	"github.com/spf13/pflag"

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
	Address string
	// Config is --config, the client file of a command that a client file may run.
	Config string
}

// Register registers --inventory, the secrets file's flags and --address, of a command that
// needs the secrets file.
func (f *NodeFlags) Register(fs *pflag.FlagSet) {
	f.ClusterFlags.Register(fs)
	f.Secrets.Register(fs)
	fs.StringVar(&f.Address, "address", "", "address of the node's chalkd, host or host:port (default the node's address in the inventory)")
}

// RegisterClient registers the flags of a command that a client file may run: those of Register
// and --config.
func (f *NodeFlags) RegisterClient(fs *pflag.FlagSet) {
	f.Register(fs)
	fs.StringVar(&f.Config, "config", "", "client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)")
}
