package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/manifest"
)

// parse parses flags anywhere among args, as in `chalkctl install w1 --insecure`, and returns the
// positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// clusterFlags select the cluster definition.
type clusterFlags struct {
	flake, cluster, manifest string
}

func (c *clusterFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.flake, "flake", ".", "directory of the flake that defines the cluster")
	fs.StringVar(&c.cluster, "cluster", "", "cluster to use when the flake defines several")
	fs.StringVar(&c.manifest, "manifest", "", "read the cluster's manifest from this file instead of evaluating the flake")
}

// cluster is an evaluated cluster definition.
type cluster struct {
	flags clusterFlags
	// attr is the flake attribute the manifest was evaluated from, such as chalkos.homelab.
	attr     string
	manifest *manifest.Manifest
	// partial is set when a client file named the nodes, without a cluster definition: the
	// manifest holds the cluster's name and the nodes' addresses alone.
	partial bool
}

// loadCluster reads the manifest with nix eval, or from the file --manifest names.
func (a *app) loadCluster(ctx context.Context, f clusterFlags) (*cluster, error) {
	if f.manifest != "" {
		data, err := os.ReadFile(f.manifest)
		if err != nil {
			return nil, err
		}
		m, err := manifest.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.manifest, err)
		}
		return &cluster{flags: f, manifest: m}, nil
	}
	name := f.cluster
	if name == "" {
		out, err := a.nix(ctx, "eval", "--json", f.flake+"#chalkos", "--apply", "builtins.attrNames")
		if err != nil {
			return nil, err
		}
		var names []string
		if err := json.Unmarshal(out, &names); err != nil {
			return nil, fmt.Errorf("list the flake's clusters: %w", err)
		}
		switch len(names) {
		case 0:
			return nil, fmt.Errorf("the flake %s defines no cluster under chalkos", f.flake)
		case 1:
			name = names[0]
		default:
			return nil, fmt.Errorf("the flake %s defines the clusters %s; choose one with --cluster", f.flake, strings.Join(names, ", "))
		}
	}
	quoted, err := attrName(name)
	if err != nil {
		return nil, err
	}
	attr := "chalkos." + quoted
	out, err := a.nix(ctx, "eval", "--json", f.flake+"#"+attr+".manifest")
	if err != nil {
		return nil, err
	}
	m, err := manifest.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	return &cluster{flags: f, attr: attr, manifest: m}, nil
}

func (c *cluster) node(name string) (manifest.Node, error) {
	n, ok := c.manifest.Nodes[name]
	if !ok {
		names := make([]string, 0, len(c.manifest.Nodes))
		for n := range c.manifest.Nodes {
			names = append(names, n)
		}
		sort.Strings(names)
		return manifest.Node{}, fmt.Errorf("the cluster %s has no node %s; its nodes are %s", c.manifest.Cluster.Name, name, strings.Join(names, ", "))
	}
	return n, nil
}

// buildImage builds a role's image and returns the directory holding it.
func (a *app) buildImage(ctx context.Context, c *cluster, role string) (string, error) {
	r, ok := c.manifest.Roles[role]
	if !ok {
		return "", fmt.Errorf("the cluster has no role %s", role)
	}
	if c.attr == "" {
		return "", errors.New("the manifest was read from a file, so the role image cannot be built; pass --image")
	}
	image := r.Image
	// The manifest names the image by the role's name unquoted, which splits a name with dots.
	if image == "roles."+role+".image" {
		quoted, err := attrName(role)
		if err != nil {
			return "", err
		}
		image = "roles." + quoted + ".image"
	}
	out, err := a.nix(ctx, "build", "--no-link", "--print-out-paths", c.flags.flake+"#"+c.attr+"."+image+"^out")
	if err != nil {
		return "", err
	}
	paths := strings.Fields(string(out))
	if len(paths) != 1 {
		return "", fmt.Errorf("nix build printed %d paths for the image of role %s, want one", len(paths), role)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		return "", fmt.Errorf("the image of role %s: %w", role, err)
	}
	return paths[0], nil
}

// attrName quotes a name as one component of a flake attribute path, so dots do not split it.
// Nix percent-decodes the fragment of a flake reference, so every byte but the URL-unreserved
// ones is percent-encoded. A Nix attribute path has no escape for a double quote.
func attrName(name string) (string, error) {
	if strings.Contains(name, `"`) {
		return "", fmt.Errorf("the name %q contains a double quote, which a Nix attribute path cannot express", name)
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// endpoint returns where the node's chalkd is reached: --endpoint, or its first static address.
func endpoint(flagValue, node string, id manifest.Identity) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	addrs := id.StaticAddresses()
	if len(addrs) == 0 {
		return "", fmt.Errorf("node %s has no static address; pass --endpoint", node)
	}
	return addrs[0], nil
}
