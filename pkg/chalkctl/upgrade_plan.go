package chalkctl

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
)

// upgradeNode is a node to upgrade, by its roles in the clusters of its layers; the image it gets;
// and the config it gets for the image, the inventory's, checked against the image; nil on a
// partial cluster, where the node carries its own over.
type upgradeNode struct {
	name     string
	roles    upgradeRoles
	image    *diskImage
	delivery *delivery
	// secret is the fallback secret the delivery's new encrypted volumes are enrolled with, asked
	// for before any node is drained; empty when it adds none.
	secret string
	// rollbackTo is the image a rollback run boots on every node, as planRollback chose it; nil
	// for an upgrade.
	rollbackTo *nodev1.BootImage
	// uncounted is set once a rollback staged its image on the node without tries, so systemd-boot
	// does not count its boots and none is found healthy.
	uncounted bool
}

// target is the image the node gets: the image to install, or the one a rollback boots; by its
// name in messages and its root hash.
func (n *upgradeNode) target() (string, []byte) {
	if n.rollbackTo != nil {
		return bootImageName(n.rollbackTo), n.rollbackTo.GetRootHash()
	}
	return n.image.name(), n.image.header.RootHash
}

// upgradeRole is a node's role in the cluster of one of its layers, with the layer's hooks.
type upgradeRole struct {
	u    layer.Upgrade
	role layer.UpgradeRole
}

// upgradeRoles are a node's roles in the clusters of its layers.
type upgradeRoles []upgradeRole

// coordinator reports whether the node acts for the others in one of its layers.
func (rs upgradeRoles) coordinator() bool {
	return slices.ContainsFunc(rs, func(r upgradeRole) bool { return r.role.Coordinator })
}

// member reports whether the node serves the cluster of one of its layers.
func (rs upgradeRoles) member() bool {
	return slices.ContainsFunc(rs, func(r upgradeRole) bool { return r.role.Member })
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
	// coordinators are the cluster's nodes that act for the others, which the layers drain and
	// release nodes through.
	coordinators []string
	// rollbackTo is the image a rollback run boots on its nodes; nil for an upgrade.
	rollbackTo *nodev1.BootImage
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
	// The coordinators a partial cluster has are known by asking them.
	probed := map[string]bool{}
	for _, name := range candidates {
		if err := c.node(name); err != nil {
			return err
		}
		info, _, roles, err := r.probe(ctx, name)
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
		n := &upgradeNode{name: name, roles: roles}
		probed[name] = roles.coordinator()
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
	return r.findCoordinators(ctx, probed)
}

// findCoordinators names the cluster's coordinators: the nodes probed, or asked now, that act for
// the others, and those the inventory lists. The coordinators drain the nodes and keep their
// cluster's quorum, so every node's role is read, also of nodes the run leaves alone. Loading the
// inventory read every node's files already, so a node whose files are broken stops every command,
// and this adds only a node whose files hold the same document twice. Without an inventory, the
// nodes --address names that the run leaves alone are asked.
func (r *upgradeRun) findCoordinators(ctx context.Context, probed map[string]bool) error {
	for _, name := range r.nodeNames() {
		if _, ok := probed[name]; r.cluster.partial() && !ok {
			_, _, roles, err := r.probe(ctx, name)
			if err != nil {
				return err
			}
			probed[name] = roles.coordinator()
		}
		listed, err := listedCoordinator(r.cluster, name)
		if err != nil {
			return err
		}
		if probed[name] || listed {
			r.coordinators = append(r.coordinators, name)
		}
	}
	if len(r.coordinators) == 0 && !r.noReboot {
		for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
			if err := r.nodes[name].noCoordinator(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// planRollback chooses the image a rollback run boots, before any node is sent anything: the one
// image the nodes named, or every node of the cluster, roll back to, as rollbackVote reads it from
// each node's status. A node without a vote must run that image already, as a node an upgrade
// leaves alone runs the upgrade's image. The image is the same on a run again after one that
// stopped, so the run continues instead of booting the nodes it rolled back forward again.
func (r *upgradeRun) planRollback(ctx context.Context, named []string) error {
	candidates := named
	if len(candidates) == 0 {
		candidates = r.nodeNames()
	}
	if len(candidates) == 0 && r.cluster.partial() {
		return errors.New("without an inventory, give each node's address with --address NODE=ADDR")
	}
	r.nodes = map[string]*upgradeNode{}
	probed := map[string]bool{}
	boots := map[string]*nodev1.BootStatus{}
	// votes are the nodes by the root hash, in hex, of the image they roll back to, and images
	// that image as a node's status names it.
	votes := map[string][]string{}
	images := map[string]*nodev1.BootImage{}
	for _, name := range candidates {
		if err := r.cluster.node(name); err != nil {
			return err
		}
		_, st, roles, err := r.probe(ctx, name)
		if err != nil {
			return err
		}
		b := st.GetBoot()
		if b.GetError() != "" {
			return fmt.Errorf("%s: its boot is unknown: %s", name, b.GetError())
		}
		probed[name], boots[name] = roles.coordinator(), b
		r.nodes[name] = &upgradeNode{name: name, roles: roles}
		if v := rollbackVote(b); v != nil {
			key := hex.EncodeToString(v.GetRootHash())
			votes[key] = append(votes[key], name)
			// A status that names the image's provenance names it best.
			if images[key] == nil || images[key].GetRevision() == "" && images[key].GetTag() == "" {
				images[key] = v
			}
		}
	}
	target, err := rollbackTarget(votes, images)
	if err != nil {
		return err
	}
	for _, name := range candidates {
		if b := boots[name]; rollbackVote(b) == nil && !bytes.Equal(b.GetRunning().GetRootHash(), target.GetRootHash()) {
			return fmt.Errorf("%s does not run %s, which the other nodes roll back to, and keeps no previous image: %s; leave it out with --nodes", name, bootImageName(target), noPrevious(b))
		}
		r.nodes[name].rollbackTo = target
	}
	r.rollbackTo = target
	return r.findCoordinators(ctx, probed)
}

// rollbackVote is the image a rollback boots on a node, as its status names it: the image a
// rollback got it to already, which it runs or boots next, or else its previous image; nil
// without either.
func rollbackVote(b *nodev1.BootStatus) *nodev1.BootImage {
	if b.GetRollback() != nil {
		return b.GetRollback()
	}
	return b.GetPrevious()
}

// rollbackTarget is the one image the votes name; an error when they name none or several.
func rollbackTarget(votes map[string][]string, images map[string]*nodev1.BootImage) (*nodev1.BootImage, error) {
	switch len(votes) {
	case 0:
		return nil, errors.New("no node keeps a previous image to roll back to: an upgrade found healthy keeps the image before it, until the next upgrade")
	case 1:
		for key := range images {
			return images[key], nil
		}
	}
	var parts []string
	for _, key := range slices.Sorted(maps.Keys(votes)) {
		parts = append(parts, strings.Join(votes[key], ", ")+" to "+bootImageName(images[key]))
	}
	return nil, fmt.Errorf("the nodes roll back to different images: %s; roll them back separately with --nodes", strings.Join(parts, "; "))
}

// noPrevious says why a node's status names no previous image.
func noPrevious(b *nodev1.BootStatus) string {
	if b.GetStaged() != nil {
		return "its inactive slot holds " + bootImageName(b.GetStaged()) + ", staged to boot next; chalkctl reboot boots it"
	}
	return "its inactive slot holds no image it booted before, as after an install or an upgrade it fell back from"
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

// split sorts the nodes to upgrade into the coordinators and the others, each by name.
func (r *upgradeRun) split() (coordinators, others []string) {
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		if r.nodes[name].roles.coordinator() {
			coordinators = append(coordinators, name)
		} else {
			others = append(others, name)
		}
	}
	return coordinators, others
}

// order is the order run upgrades the nodes in.
func (r *upgradeRun) order() []string {
	coordinators, others := r.split()
	return append(coordinators, others...)
}
