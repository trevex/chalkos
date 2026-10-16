package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/inventory"
)

func (a *app) validateCommand() *cobra.Command {
	var path string
	cmd := a.command(&cobra.Command{
		Use:     "validate [<node>...]",
		Short:   "Check nodes' config against the schemas of their images",
		Long:    validateLong,
		Example: validateExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.validate(path, pos) })
	cmd.Flags().StringVar(&path, "inventory", "", "inventory naming the nodes and their config files (default "+inventory.DefaultFile+" in the current directory)")
	return cmd
}

// validate checks the config of the nodes named, or of every node of the inventory, and names
// every node whose config is invalid.
func (a *app) validate(path string, names []string) error {
	if path == "" {
		path = inventory.DefaultFile
	}
	inv, err := inventory.Load(path)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		if names = inv.NodeNames(); len(names) == 0 {
			return fmt.Errorf("the inventory %s names no node", path)
		}
	}
	var failed []string
	for _, name := range names {
		line, err := validateNode(inv, name)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		fmt.Fprintln(a.stdout, line)
	}
	if len(failed) > 0 {
		return errors.New("the config of " + count(len(failed), "node") + " is invalid:\n" + strings.Join(failed, "\n"))
	}
	return nil
}

// validateNode checks a node's config against the image its Image names, as the node would.
func validateNode(inv *inventory.Inventory, name string) (string, error) {
	nc, err := inv.Node(name)
	if err != nil {
		return "", err
	}
	md, err := nc.Resolve()
	if err != nil {
		return "", err
	}
	c, err := imageContract(nc.ImageDir)
	if err != nil {
		return "", err
	}
	c.platform = md.Platform
	d, err := checkNodeConfig(nc, c)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: valid for the Image %s (%s on %s, root hash %.12s), config %s", name, nc.ImageName, md.Variant, md.Platform, md.RootHash, d.Version), nil
}
