package chalkctl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/strs"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/sdk"
	"github.com/trevex/chalkos/pkg/storage"
)

type validateFlags struct {
	cluster layer.ClusterFlags
	output  layer.Output
}

func (a *app) validateCommand() *cobra.Command {
	var f validateFlags
	cmd := a.command(&cobra.Command{
		Use:         "validate [<node>...]",
		Short:       "Check nodes' config against the schemas of their images",
		Long:        validateLong,
		Example:     validateExample,
		Args:        cobra.ArbitraryArgs,
		Annotations: layer.Meta{Credentials: layer.NoCredentials}.Annotations(),
	}, func(a *app, ctx context.Context, pos []string) error { return a.validate(ctx, f, pos) })
	cmd.Flags().StringVar(&f.cluster.Inventory, "inventory", "", "inventory naming the nodes and their config files (default $"+inventory.Env+", else "+inventory.DefaultFile+" in the current directory)")
	f.output.Register(cmd.Flags())
	return cmd
}

// validate checks the config of the nodes named, or of every node, and names each invalid one. The
// compiled layers check the nodes together first, as every command does on loading the inventory. A
// config that leaves the node's secrets unencrypted draws a warning, and so does what a compiled
// layer warns about the nodes together. A valid node is refused too when its config disagrees with
// another valid node of the inventory, or one not checked, on a field either node's image marks as
// held alike by every node of a cluster; each pair of nodes is named once.
func (a *app) validate(ctx context.Context, f validateFlags, names []string) error {
	path := cmp.Or(f.cluster.Path(), inventory.DefaultFile)
	c, err := readInventory(ctx, path)
	if err != nil {
		return err
	}
	inv := c.inv
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
	if err := c.check(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var failed failures
	out := validation{Nodes: []nodeValidation{}, Warnings: []string{}, Disagreements: []string{}}
	contracts := map[string]sdk.Contract{}
	invalid := map[string]bool{}
	for _, name := range nodes {
		v, ct, err := validateNode(ctx, c, name)
		if err != nil {
			failed.add(name, err)
			invalid[name] = true
			out.Nodes = append(out.Nodes, nodeValidation{Node: name, Error: err.Error(), Warnings: []string{}})
			continue
		}
		contracts[name] = ct
		out.Nodes = append(out.Nodes, v)
		if f.output.JSON() {
			continue
		}
		fmt.Fprintln(a.stdout, v.line())
		for _, w := range v.Warnings {
			fmt.Fprintln(a.stderr, "warning: "+w)
		}
	}
	out.Disagreements = disagreements(c, nodes, contracts, invalid)
	for _, p := range compiled[layer.InventoryWarnings]() {
		out.Warnings = append(out.Warnings, p.InventoryWarnings(c)...)
	}
	if f.output.JSON() {
		if err := layer.WriteJSON(a.stdout, out); err != nil {
			return err
		}
	} else {
		for _, w := range out.Warnings {
			fmt.Fprintln(a.stderr, "warning: "+w)
		}
	}
	var msgs []string
	if failed.nodes > 0 {
		msgs = append(msgs, "the config of "+strs.Count(failed.nodes, "node")+" is invalid:\n"+failed.String())
	}
	msgs = append(msgs, out.Disagreements...)
	if len(msgs) > 0 {
		return errors.New(strings.Join(msgs, "\n"))
	}
	return nil
}

// disagreements are the errors of the pairs of nodes that disagree on a field every node of a
// cluster holds alike, each pair once: every valid node among nodes, compared with its contract,
// and every other node of the inventory but the invalid ones, whose errors validate names already.
func disagreements(c *cluster, nodes []string, contracts map[string]sdk.Contract, invalid map[string]bool) []string {
	msgs := []string{}
	compared := map[[2]string]bool{}
	for _, name := range nodes {
		ct, ok := contracts[name]
		if !ok {
			continue
		}
		for _, other := range c.NodeNames() {
			if invalid[other] || compared[[2]string{other, name}] {
				continue
			}
			compared[[2]string{name, other}] = true
			if err := c.disagreement(ct, name, other); err != nil {
				msgs = append(msgs, err.Error())
			}
		}
	}
	return msgs
}

// failures are the errors of the nodes whose config is invalid, each once with the nodes it is
// the error of, in the order they first failed: a broken file the nodes share is named once.
type failures struct {
	nodes  int
	errors []*failure
}

// failure is an error and the nodes it is the error of. prefixed tells whether one of the errors
// named its node first, as "node <name>: ", which the error's line names the nodes by instead.
type failure struct {
	msg      string
	nodes    []string
	prefixed bool
}

// add records the error of a node's config.
func (f *failures) add(name string, err error) {
	f.nodes++
	msg, prefixed := strings.CutPrefix(err.Error(), "node "+name+": ")
	i := slices.IndexFunc(f.errors, func(e *failure) bool { return e.msg == msg })
	if i < 0 {
		i = len(f.errors)
		f.errors = append(f.errors, &failure{msg: msg})
	}
	f.errors[i].nodes = append(f.errors[i].nodes, name)
	f.errors[i].prefixed = f.errors[i].prefixed || prefixed
}

// String is an error per line, after the nodes it is the error of: "node n1: " for one node and
// "nodes n1, n2: " for more; an error that names its node itself stands alone.
func (f failures) String() string {
	lines := make([]string, len(f.errors))
	for i, e := range f.errors {
		switch {
		case len(e.nodes) > 1:
			lines[i] = "nodes " + strings.Join(e.nodes, ", ") + ": " + e.msg
		case e.prefixed:
			lines[i] = "node " + e.nodes[0] + ": " + e.msg
		default:
			lines[i] = e.msg
		}
	}
	return strings.Join(lines, "\n")
}

// validation is what validate -o json prints.
type validation struct {
	// Nodes are the nodes checked, in the order checked.
	Nodes []nodeValidation `json:"nodes"`
	// Warnings are what the layers warn of the nodes' config together.
	Warnings []string `json:"warnings"`
	// Disagreements are the errors of the pairs of nodes whose config disagrees on a field every
	// node of a cluster holds alike, which the nodes' own checks pass.
	Disagreements []string `json:"disagreements"`
}

// nodeValidation is the result of checking a node's config, as validate -o json prints it.
type nodeValidation struct {
	Node string `json:"node"`
	// Valid is set when the node's config passed the checks of validate.
	Valid bool `json:"valid"`
	// Error says why it did not; the fields after it are then left out.
	Error string `json:"error,omitempty"`
	// Image is the image's name in the inventory, ImageReference the manifest of an image from a
	// registry by its digest, and ImageName, Platform and RootHash the image's metadata.
	Image          string `json:"image,omitempty"`
	ImageReference string `json:"imageReference,omitempty"`
	ImageName      string `json:"imageName,omitempty"`
	Platform       string `json:"platform,omitempty"`
	RootHash       string `json:"rootHash,omitempty"`
	// ConfigVersion is the config's version, as status names it; left out when validate does not
	// know it, as for Secret values from sources it does not decrypt.
	ConfigVersion string `json:"configVersion,omitempty"`
	// Warnings are what the node's valid config warrants a warning for.
	Warnings []string `json:"warnings"`
}

// line is the line validate prints of a node whose config is valid.
func (v nodeValidation) line() string {
	image := v.Image
	if v.ImageReference != "" {
		// The tag may move; the line names the manifest the config was checked against.
		image += " at " + v.ImageReference
	}
	version := "config " + v.ConfigVersion
	if v.ConfigVersion == "" {
		version = "config version unknown"
	}
	return fmt.Sprintf("%s: valid for the image %s (%s on %s, root hash %.12s), %s", v.Node, image, v.ImageName, v.Platform, v.RootHash, version)
}

// validateNode checks a node's config against the image the inventory names for it, as the node
// would, with what a valid config warrants a warning for, and the contract of the image it checked
// against. Its errors and warnings name the node.
func validateNode(ctx context.Context, cl *cluster, name string) (nodeValidation, sdk.Contract, error) {
	_, md, err := cl.image(name)
	if err != nil {
		return nodeValidation{}, sdk.Contract{}, err
	}
	nc, encrypted, err := cl.checkedConfig(name)
	if err != nil {
		return nodeValidation{}, sdk.Contract{}, err
	}
	c, err := cl.imageContractOf(name)
	if err != nil {
		return nodeValidation{}, sdk.Contract{}, fmt.Errorf("node %s: %w", name, err)
	}
	d, err := sdk.Check(ctx, c, nc.Name, nc.Documents)
	if err != nil {
		return nodeValidation{}, sdk.Contract{}, fmt.Errorf("node %s: %w", name, err)
	}
	v := nodeValidation{
		Node: name, Valid: true, Image: nc.ImageName, ImageName: md.Name, Platform: md.Platform, RootHash: md.RootHash,
		ConfigVersion: d.Version, Warnings: []string{},
	}
	for _, w := range d.Unpinned {
		v.Warnings = append(v.Warnings, fmt.Sprintf("node %s: %s", name, w))
	}
	// STATE follows the SystemDisk's encryption mode, and holds the node's keys and secrets.
	if d.Identity.Storage.Encryption == storage.EncryptionNone {
		v.Warnings = append(v.Warnings, fmt.Sprintf("node %s: its SystemDisk's encryption.mode is none, so STATE, which holds the node's keys and secrets, is not encrypted", name))
	}
	if nc.ImageOCI != "" {
		a, err := nc.Artifact()
		if err != nil {
			return nodeValidation{}, sdk.Contract{}, err
		}
		v.ImageReference = a.Reference
	}
	if len(encrypted) > 0 {
		// The version hashes the Secret values, which validate does not decrypt.
		v.ConfigVersion = ""
		v.Warnings = append(v.Warnings, fmt.Sprintf("node %s: used placeholders for %s, as validate decrypts no sops or age source; chalkctl config apply --dry-run %s reads their sources", name, strs.And(encrypted), name))
	}
	return v, c, nil
}
