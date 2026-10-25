package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

// upgradeNode is a node to upgrade, by its part in Kubernetes: its kind, and whether it is part
// of a cluster, so its Node exists; the image it gets; and the config it gets for the image, the
// inventory's, checked against the image; nil on a partial cluster, where the node carries its own
// over.
type upgradeNode struct {
	name, kind string
	inCluster  bool
	image      *diskImage
	delivery   *delivery
	// secret is the fallback secret the delivery's new encrypted volumes are enrolled with, asked
	// for before any node is drained; empty when it adds none.
	secret string
}

// upgradeGroup is the nodes that get one image: the image given, or the one an image directory of
// the inventory holds.
type upgradeGroup struct {
	// key tells groups apart: the image directory, the reference by digest of an image in a
	// registry, or empty for the image given.
	key, dir          string
	variant, platform string
	nodes             []string
	image             *diskImage
}

// upgradePlan is what a run upgrades, as the nodes were when it started.
type upgradePlan struct {
	// nodes are the nodes to upgrade, and groups the same nodes by the image they get.
	nodes  map[string]*upgradeNode
	groups []*upgradeGroup
	// controlPlanes are the cluster's control-plane nodes, which drain and uncordon nodes.
	controlPlanes []string
}

// plan finds the nodes to upgrade and opens the image of each group from its directory, which
// must hold the build its metadata.json names.
func (r *upgradeRun) plan(ctx context.Context, named []string, img *diskImage) error {
	if err := r.find(ctx, named, img); err != nil {
		return err
	}
	for _, g := range r.groups {
		if g.image == nil {
			var err error
			if g.image, err = openImage(ctx, g.dir, false, r.a.stderr); err != nil {
				return err
			}
			if err := r.cluster.sameBuild(g.nodes[0], g.image); err != nil {
				return err
			}
		}
		for _, name := range g.nodes {
			r.nodes[name].image = g.image
		}
	}
	return nil
}

// closeImages closes the images plan opened from the groups' directories.
func (p *upgradePlan) closeImages() {
	for _, g := range p.groups {
		if g.dir != "" && g.image != nil {
			g.image.Close()
		}
	}
}

// find picks the nodes to upgrade and groups them by the image they get. It asks every node what
// it runs before any node is sent anything.
//   - The nodes are those named, else every node of the inventory, else every node --address
//     names. With an image and no nodes named, only those that run the image's variant.
//   - Without an image each node gets its image in the inventory. A node whose platform differs
//     from that image's stops the run.
//   - With an image, a node on another platform is skipped and named, or stops the run when named.
//   - A node may get an image of another variant than it runs, which is printed. The node checks
//     its config against the image before the image becomes the next boot.
func (r *upgradeRun) find(ctx context.Context, named []string, img *diskImage) error {
	c := r.cluster
	candidates := named
	if len(candidates) == 0 {
		candidates = r.nodeNames()
	}
	if len(candidates) == 0 && c.partial() {
		return errors.New("without an inventory, give each node's address with --address NODE=ADDR")
	}
	r.nodes = map[string]*upgradeNode{}
	groups := map[string]*upgradeGroup{}
	var skipped []string
	skip := func(name, platform string) error {
		if len(named) > 0 {
			return fmt.Errorf("%s runs on %s; the image is built for %s", name, platform, img.Info.Platform())
		}
		skipped = append(skipped, fmt.Sprintf("%s (%s)", name, platform))
		return nil
	}
	// The control planes a partial cluster has are known by asking them.
	probedCPs := map[string]bool{}
	for _, name := range candidates {
		if err := c.node(name); err != nil {
			return err
		}
		info, st, err := r.probe(ctx, name)
		if err != nil {
			return err
		}
		// Without an image a node gets its image in the inventory, which must be built for the platform
		// it runs.
		if img == nil {
			if err := c.samePlatform(name, info.Platform); err != nil {
				return err
			}
		}
		n := &upgradeNode{name: name}
		if k := st.Kubernetes; k != nil && hasKubernetes(info, st) {
			n.kind, n.inCluster = k.Kind, inCluster(k)
		}
		probedCPs[name] = n.kind == kubernetes.KindControlPlane
		if img != nil && len(named) == 0 && info.Variant != img.Info.Variant() {
			continue
		}
		if img != nil && info.Platform != img.Info.Platform() {
			if err := skip(name, info.Platform); err != nil {
				return err
			}
			continue
		}
		g, err := r.groupOf(name, img)
		if err != nil {
			return err
		}
		if groups[g.key] == nil {
			groups[g.key] = g
		}
		g = groups[g.key]
		r.nodes[name] = n
		if info.Variant != "" && info.Variant != g.variant {
			r.say("%s runs the variant %s; it gets the image of %s", name, info.Variant, g.variant)
		}
		g.nodes = append(g.nodes, name)
	}
	if len(skipped) > 0 {
		r.say("skipping %s: the image is built for %s", strings.Join(skipped, ", "), img.Info.Platform())
	}
	if len(r.nodes) == 0 {
		if img != nil {
			return fmt.Errorf("the cluster has no node of the variant %s on %s to upgrade", img.Info.Variant(), img.Info.Platform())
		}
		return errors.New("the cluster has no node to upgrade")
	}
	for _, key := range slices.Sorted(maps.Keys(groups)) {
		r.groups = append(r.groups, groups[key])
	}
	// The control planes drain the nodes and keep etcd's quorum, so every node's type is read,
	// also of nodes the run leaves alone. Loading the inventory read every node's files already,
	// so a node whose files are broken stops every command, and this adds only a node whose
	// files hold the same document twice. Without an inventory, the nodes --address names that
	// the run leaves alone are asked.
	for _, name := range r.nodeNames() {
		if _, probed := probedCPs[name]; c.partial() && !probed {
			info, st, err := r.probe(ctx, name)
			if err != nil {
				return err
			}
			probedCPs[name] = hasKubernetes(info, st) && st.Kubernetes.GetKind() == kubernetes.KindControlPlane
		}
		kind, err := c.kind(name)
		if err != nil {
			return err
		}
		if probedCPs[name] || kind == kubernetes.KindControlPlane {
			r.controlPlanes = append(r.controlPlanes, name)
		}
	}
	if len(r.controlPlanes) == 0 && !r.noReboot {
		for _, n := range r.nodes {
			if n.inCluster {
				return fmt.Errorf("%s is drained through a control plane, but the cluster has none to ask; pass --inventory, or give a control plane's address with --address", n.name)
			}
		}
	}
	return nil
}

// nodeNames are the cluster's nodes: the inventory's, or without one those --address names.
func (r *upgradeRun) nodeNames() []string {
	if r.cluster.partial() {
		return slices.Sorted(maps.Keys(r.reach.addresses))
	}
	return r.cluster.nodeNames()
}

// groupOf is the group of the image a node gets: the image given, or the one the inventory names
// for it: from its directory, or from a registry, which the node pulls.
func (r *upgradeRun) groupOf(name string, img *diskImage) (*upgradeGroup, error) {
	if img != nil {
		return &upgradeGroup{variant: img.Info.Variant(), platform: img.Info.Platform(), image: img}, nil
	}
	nc, err := r.cluster.config(name)
	if err != nil {
		return nil, err
	}
	md, err := nc.Resolve()
	if err != nil {
		return nil, err
	}
	if nc.ImageOCI != "" {
		img, err := ociImage(nc, false)
		if err != nil {
			return nil, err
		}
		return &upgradeGroup{key: img.source.GetOci().GetReference(), variant: md.Variant, platform: md.Platform, image: img}, nil
	}
	return &upgradeGroup{key: nc.ImageDir, dir: nc.ImageDir, variant: md.Variant, platform: md.Platform}, nil
}

// split sorts the nodes to upgrade into the control planes and the others, each by name.
func (r *upgradeRun) split() (controlPlanes, others []string) {
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		if r.nodes[name].kind == kubernetes.KindControlPlane {
			controlPlanes = append(controlPlanes, name)
		} else {
			others = append(others, name)
		}
	}
	return controlPlanes, others
}

// order is the order run upgrades the nodes in.
func (r *upgradeRun) order() []string {
	controlPlanes, others := r.split()
	return append(controlPlanes, others...)
}
