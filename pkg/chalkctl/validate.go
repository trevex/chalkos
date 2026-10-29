package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/storage"
)

type validateFlags struct{ inventory string }

func (a *app) validateCommand() *cobra.Command {
	var f validateFlags
	cmd := a.command(&cobra.Command{
		Use:     "validate [<node>...]",
		Short:   "Check nodes' config against the schemas of their images",
		Long:    validateLong,
		Example: validateExample,
		Args:    cobra.ArbitraryArgs,
	}, func(a *app, ctx context.Context, pos []string) error { return a.validate(ctx, f, pos) })
	cmd.Flags().StringVar(&f.inventory, "inventory", "", "inventory naming the nodes and their config files (default "+inventory.DefaultFile+" in the current directory)")
	return cmd
}

// validate checks the config of the nodes named, or of every node of the inventory, and names
// every node whose config is invalid. The nodes' Cluster and Flannel specs are checked first, as
// every other command checks them when it loads the inventory. A valid config that
// leaves the node's secrets unencrypted draws a warning.
func (a *app) validate(ctx context.Context, f validateFlags, names []string) error {
	path := f.inventory
	if path == "" {
		path = inventory.DefaultFile
	}
	inv, err := inventory.Load(path)
	if err != nil {
		return err
	}
	inv.SetContext(ctx)
	// A node named twice is checked once; a node the inventory does not have is a usage error,
	// before any node is checked.
	var nodes []string
	for _, name := range names {
		if _, ok := inv.Spec.Nodes[name]; !ok {
			return cli.Usagef("the inventory %s has no node %s; its nodes are %s", path, name, strings.Join(inv.NodeNames(), ", "))
		}
		if !slices.Contains(nodes, name) {
			nodes = append(nodes, name)
		}
	}
	if len(nodes) == 0 {
		if nodes = inv.NodeNames(); len(nodes) == 0 {
			return fmt.Errorf("the inventory %s names no node", path)
		}
	}
	if _, err := inv.Endpoint(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var failed []string
	for _, name := range nodes {
		line, warnings, err := validateNode(inv, name)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		fmt.Fprintln(a.stdout, line)
		for _, w := range warnings {
			fmt.Fprintln(a.stderr, "warning: "+w)
		}
	}
	if len(failed) > 0 {
		return errors.New("the config of " + layer.Count(len(failed), "node") + " is invalid:\n" + strings.Join(failed, "\n"))
	}
	return nil
}

// validateNode checks a node's config against the image the inventory names for it, as the node
// would, and returns what a valid config warrants a warning for. Its errors and warnings name the
// node.
func validateNode(inv *inventory.Inventory, name string) (string, []string, error) {
	nc, err := inv.Node(name)
	if err != nil {
		return "", nil, err
	}
	md, err := nc.Resolve()
	if err != nil {
		return "", nil, err
	}
	c, err := nodeImageContract(nc, md)
	if err != nil {
		return "", nil, fmt.Errorf("node %s: %w", name, err)
	}
	d, err := checkNodeConfig(nc, c)
	if err != nil {
		return "", nil, fmt.Errorf("node %s: %w", name, err)
	}
	var warnings []string
	// STATE follows the SystemDisk's encryption mode, and holds the node's keys and secrets.
	if d.identity.Storage.Encryption == storage.EncryptionNone {
		warnings = append(warnings, fmt.Sprintf("node %s: its SystemDisk's encryption.mode is none, so STATE, which holds the node's keys and secrets, is not encrypted", name))
	}
	image := nc.ImageName
	if nc.ImageOCI != "" {
		// The tag may move; the line names the manifest the config was checked against.
		a, err := nc.Artifact()
		if err != nil {
			return "", nil, err
		}
		image += " at " + a.Reference
	}
	line := fmt.Sprintf("%s: valid for the image %s (%s on %s, root hash %.12s), config %s", name, image, md.Variant, md.Platform, md.RootHash, d.Version)
	return line, warnings, nil
}
