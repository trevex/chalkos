package chalkctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/manifest"
)

// upgradeRun upgrades nodes to the images of their roles and platforms.
type upgradeRun struct {
	a       *app
	cluster *cluster
	creds   *credentials
	// endpoints are the nodes' chalkd addresses that --endpoint gives.
	endpoints map[string]string
	// nodes are the nodes to upgrade, as they were when the run started, and groups the same
	// nodes by the image they get.
	nodes  map[string]*upgradeNode
	groups []*upgradeGroup
	// controlPlanes are the cluster's control-plane nodes, which drain and uncordon nodes.
	controlPlanes           []string
	allowDowntime, noReboot bool
	// retryFailed installs the image again on nodes that fell back from it; each node is upgraded
	// once per run, so a second fall back stops the run.
	retryFailed, deleteEmptyDir             bool
	maxUnavailable                          int
	timeout, drainTimeout, poll, quorumWait time.Duration
	out                                     sync.Mutex
}

// upgradeNode is a node to upgrade, by its part in Kubernetes: its kind, and whether it is part
// of a cluster, so its Node exists; and the image it gets.
type upgradeNode struct {
	name, kind string
	inCluster  bool
	image      *diskImage
}

// upgradeGroup is the nodes of a role on a platform, which get one image.
type upgradeGroup struct {
	role, platform string
	nodes          []string
	image          *diskImage
}

type upgradeFlags struct {
	cluster                                 clusterFlags
	secrets                                 secretFlags
	config, image, signKey, signCert, nodes string
	endpoints                               endpointList
	maxUnavailable                          int
	allowDowntime, noReboot, retryFailed    bool
	deleteEmptyDir                          bool
	timeout                                 time.Duration
}

func (a *app) upgradeCommand() *cobra.Command {
	f := upgradeFlags{endpoints: endpointList{}}
	cmd := a.command(&cobra.Command{
		Use:     "upgrade",
		Short:   "Install new images on the cluster's nodes, one control plane at a time",
		Long:    upgradeLong,
		Example: upgradeExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.upgrade(ctx, f, pos) })
	fs := cmd.Flags()
	f.cluster.register(fs)
	f.secrets.register(fs)
	fs.StringVar(&f.config, "config", "", "client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)")
	fs.Var(f.endpoints, "endpoint", "address of a node's chalkd, `NODE=ADDR`, host or host:port; may be repeated (default each node's first static address, or the address a client file that prefers its addresses names)")
	fs.StringVar(&f.image, "image", "", "image to install on the nodes of its role and platform: a raw image with repart-output.json next to it, or the directory nix build produces (default: build each node's image from the cluster definition)")
	fs.StringVar(&f.signKey, "sign-key", "", "PEM key of the Secure Boot db signer, to sign the images' UKIs")
	fs.StringVar(&f.signCert, "sign-cert", "", "PEM certificate of the Secure Boot db signer")
	fs.StringVar(&f.nodes, "nodes", "", "comma-separated nodes to upgrade (default every node of the cluster, or with --image every node of the image's role)")
	fs.IntVar(&f.maxUnavailable, "max-unavailable", 1, "how many workers and nodes without Kubernetes upgrade at once")
	fs.BoolVar(&f.allowDowntime, "allow-downtime", false, "upgrade one or two control planes, whose etcd loses its quorum and API server is down while one reboots")
	fs.BoolVar(&f.noReboot, "no-reboot", false, "install the images on the nodes without draining or rebooting them; they boot with their next reboot")
	fs.BoolVar(&f.retryFailed, "retry-failed", false, "install an image again, once, on nodes that fell back from it")
	fs.BoolVar(&f.deleteEmptyDir, "delete-emptydir-data", false, "evict pods with emptyDir volumes too, deleting their data")
	fs.DurationVar(&f.timeout, "timeout", 30*time.Minute, "how long to wait for each node to come back healthy and for its drain")
	return cmd
}

func (a *app) upgrade(ctx context.Context, f upgradeFlags, pos []string) error {
	cf, sf, endpoints := f.cluster, f.secrets, f.endpoints
	if len(pos) != 0 || f.maxUnavailable < 1 {
		return errors.New("usage: chalkctl upgrade [--image PATH] [--nodes N,...] [--max-unavailable N] [--allow-downtime] [--no-reboot] [--retry-failed] [--delete-emptydir-data]")
	}
	creds, err := a.loadCredentials(ctx, sf, f.config, cf.flake)
	if err != nil {
		return err
	}
	c, err := a.clusterFor(ctx, cf, creds)
	if err != nil {
		return err
	}
	for name := range endpoints {
		if _, err := c.node(name); err != nil {
			return err
		}
	}
	var img *diskImage
	if f.image != "" {
		if img, err = openImage(ctx, f.image, f.signKey, f.signCert, false); err != nil {
			return err
		}
		defer img.Close()
		if err := checkImage(c, img, f.signCert); err != nil {
			return err
		}
	} else if c.attr == "" {
		return errors.New("without --image the images are built from the cluster definition, which a manifest file or a client file does not give; pass --image, or --flake")
	}
	r := &upgradeRun{
		a: a, cluster: c, creds: creds, endpoints: endpoints,
		allowDowntime: f.allowDowntime, noReboot: f.noReboot, maxUnavailable: f.maxUnavailable,
		retryFailed: f.retryFailed, deleteEmptyDir: f.deleteEmptyDir,
		timeout: f.timeout, drainTimeout: f.timeout, poll: a.poll(), quorumWait: min(f.timeout, time.Minute),
	}
	var named []string
	if f.nodes != "" {
		named = strings.Split(f.nodes, ",")
	}
	if err := r.find(ctx, named, img); err != nil {
		return err
	}
	for _, g := range r.groups {
		if g.image == nil {
			path, err := a.buildImage(ctx, c, g.role, g.platform)
			if err != nil {
				return err
			}
			if g.image, err = openImage(ctx, path, f.signKey, f.signCert, false); err != nil {
				return err
			}
			defer g.image.Close()
			if err := checkImage(c, g.image, f.signCert); err != nil {
				return err
			}
			if got := g.image.info; got.Role() != g.role || got.Platform() != g.platform {
				return fmt.Errorf("the image built for the role %s on %s is of the role %s on %s", g.role, g.platform, got.Role(), got.Platform())
			}
		}
		for _, name := range g.nodes {
			r.nodes[name].image = g.image
		}
	}
	return r.run(ctx)
}

// checkImage checks the image against the cluster: its cluster, and the signatures of its UKI and
// boot loader when the cluster names a db certificate, or else when chalkctl signed them.
func checkImage(c *cluster, img *diskImage, signCert string) error {
	if got := img.info.Cluster(); got != c.manifest.Cluster.Name {
		return fmt.Errorf("the image is of the cluster %s, not %s", got, c.manifest.Cluster.Name)
	}
	certPEM := c.manifest.SecureBoot.SignerCertificate
	if certPEM == "" && signCert != "" {
		data, err := os.ReadFile(signCert)
		if err != nil {
			return err
		}
		certPEM = string(data)
	}
	if certPEM == "" {
		return nil
	}
	return img.checkSignature(certPEM)
}

// say prints a line of the run's progress, from any node's goroutine.
func (r *upgradeRun) say(format string, args ...any) {
	r.out.Lock()
	defer r.out.Unlock()
	fmt.Fprintf(r.a.stdout, format+"\n", args...)
}

// call runs fn against a node.
func (r *upgradeRun) call(name string, fn func(conn *client.Conn) error) error {
	t, err := targetIn(r.cluster, nodeCommand{endpoint: r.endpoints[name]}, name, r.creds)
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := fn(conn); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// probe reads what a node runs and its status.
func (r *upgradeRun) probe(ctx context.Context, name string) (*nodev1.InfoResponse, *nodev1.StatusResponse, error) {
	var info *nodev1.InfoResponse
	var st *nodev1.StatusResponse
	err := r.call(name, func(conn *client.Conn) error {
		i, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
		if err != nil {
			return err
		}
		s, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			return err
		}
		info, st = i.Msg, s.Msg
		return nil
	})
	return info, st, err
}

// find picks the nodes to upgrade, those named or else every node of the cluster, with an image
// every node of its role, and groups them by the role and platform they run. Every node is asked
// what it runs before any is sent anything: one that runs another role or platform than the
// cluster definition declares stops the run, with an image too. With an image, a node of its role
// that runs another platform is skipped and named with the platform it runs; a named one stops the
// run. A client file without a cluster definition
// knows no roles or platforms, so its nodes are asked alone.
func (r *upgradeRun) find(ctx context.Context, named []string, img *diskImage) error {
	m := r.cluster.manifest
	candidates := named
	if len(candidates) == 0 {
		for _, name := range slices.Sorted(maps.Keys(m.Nodes)) {
			if r.cluster.partial || img == nil || m.Nodes[name].Role == img.info.Role() {
				candidates = append(candidates, name)
			}
		}
	}
	r.nodes = map[string]*upgradeNode{}
	groups := map[[2]string]*upgradeGroup{}
	var skipped []string
	skip := func(name, platform string) error {
		if len(named) > 0 {
			return fmt.Errorf("%s runs on %s; the image is built for %s", name, platform, img.info.Platform())
		}
		skipped = append(skipped, fmt.Sprintf("%s (%s)", name, platform))
		return nil
	}
	// The control planes a client file names are known by asking them.
	probedCPs := map[string]bool{}
	for _, name := range candidates {
		node, err := r.cluster.node(name)
		if err != nil {
			return err
		}
		if !r.cluster.partial && img != nil && node.Role != img.info.Role() {
			return fmt.Errorf("%s is a node of the role %s; the image is of %s", name, node.Role, img.info.Role())
		}
		info, st, err := r.probe(ctx, name)
		if err != nil {
			return err
		}
		switch {
		case info.Cluster != m.Cluster.Name:
			return fmt.Errorf("%s runs an image of the cluster %q, not %s", name, info.Cluster, m.Cluster.Name)
		case !r.cluster.partial && info.Platform != node.Platform:
			return fmt.Errorf("%s runs an image built for %s, but the cluster definition declares it on %s; changing a node's platform is a reinstall", name, info.Platform, node.Platform)
		case !r.cluster.partial && info.Role != node.Role:
			return fmt.Errorf("%s runs an image of the role %q, but the cluster definition declares %s", name, info.Role, node.Role)
		case img != nil && info.Role != img.info.Role() && len(named) > 0:
			return fmt.Errorf("%s runs an image of the role %q; the image is of %s", name, info.Role, img.info.Role())
		}
		n := &upgradeNode{name: name}
		if k := st.Kubernetes; k != nil {
			n.kind, n.inCluster = k.Kind, inCluster(k)
		}
		probedCPs[name] = n.kind == manifest.KindControlPlane
		if img != nil && info.Role != img.info.Role() {
			continue
		}
		if img != nil && info.Platform != img.info.Platform() {
			if err := skip(name, info.Platform); err != nil {
				return err
			}
			continue
		}
		r.nodes[name] = n
		key := [2]string{info.Role, info.Platform}
		if groups[key] == nil {
			groups[key] = &upgradeGroup{role: info.Role, platform: info.Platform, image: img}
		}
		groups[key].nodes = append(groups[key].nodes, name)
	}
	if len(skipped) > 0 {
		r.say("skipping %s: the image is built for %s", strings.Join(skipped, ", "), img.info.Platform())
	}
	if len(r.nodes) == 0 {
		if img != nil {
			return fmt.Errorf("the cluster has no node of the role %s on %s to upgrade", img.info.Role(), img.info.Platform())
		}
		return errors.New("the cluster has no node to upgrade")
	}
	for _, key := range slices.SortedFunc(maps.Keys(groups), func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) }) {
		r.groups = append(r.groups, groups[key])
	}
	for _, name := range slices.Sorted(maps.Keys(m.Nodes)) {
		if probedCPs[name] || !r.cluster.partial && m.Roles[m.Nodes[name].Role].Kind == manifest.KindControlPlane {
			r.controlPlanes = append(r.controlPlanes, name)
		}
	}
	return nil
}

// run upgrades the control planes one at a time, then the other nodes in batches.
func (r *upgradeRun) run(ctx context.Context) error {
	var controlPlanes, others []string
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		if r.nodes[name].kind == manifest.KindControlPlane {
			controlPlanes = append(controlPlanes, name)
		} else {
			others = append(others, name)
		}
	}
	versions := map[string]bool{}
	for _, g := range r.groups {
		r.say("upgrading %s on %s to %s %s: %s", g.role, g.platform, g.image.info.ID(), g.image.info.Version(), strings.Join(g.nodes, ", "))
		versions[g.image.info.Version()] = true
	}
	for _, name := range controlPlanes {
		if err := r.node(ctx, r.nodes[name]); err != nil {
			return err
		}
	}
	for batch := range slices.Chunk(others, r.maxUnavailable) {
		errs := make([]error, len(batch))
		var wg sync.WaitGroup
		for i, name := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = r.node(ctx, r.nodes[name])
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
	version := strings.Join(slices.Sorted(maps.Keys(versions)), ", ")
	if r.noReboot {
		r.say("installed %s on %s; it boots with their next reboot", version, count(len(r.nodes), "node"))
	} else {
		r.say("upgraded %s to %s", count(len(r.nodes), "node"), version)
	}
	return nil
}

// node upgrades one node, continuing where an earlier run stopped: a node that runs the image
// and was found healthy is uncordoned once its Node is Ready, one that booted it is waited for,
// and the image installed already is not sent again.
func (r *upgradeRun) node(ctx context.Context, n *upgradeNode) error {
	version := n.image.info.Version()
	info, st, err := r.probe(ctx, n.name)
	if err != nil {
		return err
	}
	boot := st.GetBoot()
	runs := info.Version == version
	switch {
	case boot.GetError() != "":
		return fmt.Errorf("%s: its boot is unknown: %s", n.name, boot.GetError())
	case boot.GetFailed() == version && !r.retryFailed:
		return r.rolledBack(n, boot)
	case runs && !bytes.Equal(boot.GetRootHash(), n.image.header.RootHash):
		return r.otherBuild(n, boot)
	case runs && boot.GetBlessed():
		if r.noReboot {
			r.say("%s: runs %s", n.name, version)
			return nil
		}
		// An earlier run may have stopped while the node was not Ready; it is released only once
		// it is.
	case runs:
		r.say("%s: booted %s; waiting for it to be found healthy", n.name, version)
	default:
		if boot.GetFailed() == version {
			r.say("%s: fell back from %s before; trying it again", n.name, version)
		}
		if n.kind == manifest.KindControlPlane && !r.noReboot {
			if err := r.quorum(ctx, n.name); err != nil {
				return err
			}
		}
		if n.kind == manifest.KindWorker && !r.noReboot {
			if err := r.drain(ctx, n); err != nil {
				return err
			}
		}
		if boot.GetStaged() == version {
			r.say("%s: %s is installed already", n.name, version)
		} else if err := r.install(ctx, n); err != nil {
			return err
		}
		if r.noReboot {
			return nil
		}
		if n.kind == manifest.KindControlPlane {
			if err := r.drain(ctx, n); err != nil {
				return err
			}
		}
		if err := r.call(n.name, func(conn *client.Conn) error {
			_, err := conn.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{}))
			return err
		}); err != nil {
			return err
		}
		r.say("%s: rebooting into %s", n.name, version)
	}
	if err := r.waitHealthy(ctx, n); err != nil {
		return err
	}
	return r.uncordon(ctx, n)
}

// install sends the node its image.
func (r *upgradeRun) install(ctx context.Context, n *upgradeNode) error {
	name, img := n.name, n.image
	r.say("%s: installing %s", name, img.info.Version())
	parts, done, err := img.parts()
	if err != nil {
		return err
	}
	defer done()
	var resp *nodev1.UpgradeResponse
	err = r.call(name, func(conn *client.Conn) error {
		stream := conn.Upgrade(ctx)
		if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: &nodev1.UpgradeHeader{Image: img.header}}}); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if err := sendChunks(parts, func(c *nodev1.ImageChunk) error {
			return stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Chunk{Chunk: c}})
		}); err != nil {
			return err
		}
		res, err := stream.CloseAndReceive()
		if err == nil {
			resp = res.Msg
		}
		return err
	})
	if err != nil {
		return err
	}
	if resp.AlreadyInstalled {
		r.say("%s: runs %s already", name, img.info.Version())
	} else {
		r.say("%s: installed %s, which boots next as %s", name, img.info.Version(), resp.Entry)
	}
	return nil
}

// quorum checks that etcd keeps its quorum while the control plane reboots: its other voters
// must be healthy and enough for a quorum. With one or two voters the others can never be enough,
// and the API server is down while it reboots, which only --allow-downtime accepts. etcd may need
// a moment after the control plane before came back, so the check is tried for a while.
func (r *upgradeRun) quorum(ctx context.Context, name string) error {
	deadline := time.Now().Add(r.quorumWait)
	for {
		err := r.quorumOnce(ctx, name)
		var final finalError
		if err == nil || errors.As(err, &final) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.poll):
		}
	}
}

// finalError is a check's error that waiting does not change.
type finalError struct{ error }

func (r *upgradeRun) quorumOnce(ctx context.Context, name string) error {
	var members []*nodev1.EtcdMember
	err := r.viaControlPlanes(name, tryNextControlPlane, func(conn *client.Conn) error {
		resp, err := conn.EtcdMembers(ctx, connect.NewRequest(&nodev1.EtcdMembersRequest{}))
		if err == nil {
			members = resp.Msg.Members
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("etcd's members: %w", err)
	}
	voters, healthy, own := 0, 0, false
	for _, m := range members {
		switch {
		case m.Learner:
			return fmt.Errorf("etcd member %s is a learner yet; upgrade once it joined", m.Name)
		case m.Name == name:
			own = true
		case m.Unhealthy == "":
			healthy++
		}
		voters++
	}
	quorum := voters/2 + 1
	switch {
	case !own:
		// A control plane that is no etcd member takes nothing of etcd down.
		return nil
	case voters-1 < quorum && !r.allowDowntime:
		return finalError{fmt.Errorf("etcd has %s, and without %s fewer than the %d its quorum needs: it and the API server are down while %s reboots; pass --allow-downtime to accept that", count(voters, "voter"), name, quorum, name)}
	case voters-1 < quorum:
		return nil
	case healthy < quorum:
		return fmt.Errorf("without %s etcd has %s of %d, fewer than the %d its quorum needs", name, count(healthy, "healthy voter"), voters, quorum)
	}
	return nil
}

// viaControlPlanes calls fn on a control plane: the node itself first when it is one, then the
// others, moving on past those whose error next accepts.
func (r *upgradeRun) viaControlPlanes(prefer string, next func(error) bool, fn func(conn *client.Conn) error) error {
	names := slices.Clone(r.controlPlanes)
	if i := slices.Index(names, prefer); i > 0 {
		names = append([]string{prefer}, slices.Delete(names, i, i+1)...)
	}
	if len(names) == 0 {
		return errors.New("the cluster has no control-plane node to ask; pass --flake or --manifest")
	}
	var last error
	for _, name := range names {
		err := r.call(name, fn)
		if err == nil || !next(err) {
			return err
		}
		last = err
	}
	return fmt.Errorf("no control-plane node answered: %w", last)
}

// drainElsewhere reports whether a drain that failed so may be run on another control plane: not
// after it timed out, when the node's pods may still be stopping and another drain would only
// wait as long again.
func drainElsewhere(err error) bool {
	return connect.CodeOf(err) != connect.CodeDeadlineExceeded && tryNextControlPlane(err)
}

// drain cordons a node of the cluster and evicts its pods.
func (r *upgradeRun) drain(ctx context.Context, n *upgradeNode) error {
	if !n.inCluster {
		return nil
	}
	var resp *nodev1.DrainNodeResponse
	err := r.viaControlPlanes(n.name, drainElsewhere, func(conn *client.Conn) error {
		res, err := conn.DrainNode(ctx, connect.NewRequest(&nodev1.DrainNodeRequest{Node: n.name, TimeoutSeconds: uint32(r.drainTimeout / time.Second), DeleteEmptydirData: r.deleteEmptyDir}))
		if err == nil {
			resp = res.Msg
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("drain %s: %w", n.name, err)
	}
	cordon := "cordoned"
	if !resp.Marked {
		cordon = "was cordoned before and stays so"
	}
	pods := "pods"
	if len(resp.Evicted) == 1 {
		pods = "pod"
	}
	r.say("%s: %s, evicted %d %s, kept %d", n.name, cordon, len(resp.Evicted), pods, len(resp.Kept)+len(resp.Unmanaged))
	if len(resp.Unmanaged) > 0 {
		r.say("%s: keeps pods without a controller: %s; nothing starts them elsewhere, and they are deleted for good if the node stays down longer than their tolerations allow", n.name, strings.Join(resp.Unmanaged, ", "))
	}
	return nil
}

// uncordon makes a node of the cluster schedulable again, when the upgrade cordoned it.
func (r *upgradeRun) uncordon(ctx context.Context, n *upgradeNode) error {
	if !n.inCluster {
		return nil
	}
	var uncordoned bool
	err := r.viaControlPlanes(n.name, tryNextControlPlane, func(conn *client.Conn) error {
		resp, err := conn.UncordonNode(ctx, connect.NewRequest(&nodev1.UncordonNodeRequest{Node: n.name}))
		if err == nil {
			uncordoned = resp.Msg.Uncordoned
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("uncordon %s: %w", n.name, err)
	}
	if uncordoned {
		r.say("%s: uncordoned", n.name)
	}
	return nil
}

// waitHealthy waits until the node runs the image, its boot was found healthy, and a node in
// the cluster is Ready, or until it fell back.
func (r *upgradeRun) waitHealthy(ctx context.Context, n *upgradeNode) error {
	version := n.image.info.Version()
	deadline := time.Now().Add(r.timeout)
	var last error
	for {
		info, st, err := r.probe(ctx, n.name)
		switch {
		case err != nil:
			last = err
		case st.GetBoot().GetFailed() == version:
			return r.rolledBack(n, st.GetBoot())
		case info.Version != version:
			last = fmt.Errorf("it runs %s", info.Version)
		case !bytes.Equal(st.GetBoot().GetRootHash(), n.image.header.RootHash):
			// Waiting does not change the build it booted.
			return r.otherBuild(n, st.GetBoot())
		case !st.GetBoot().GetBlessed():
			last = errors.New("its boot was not found healthy yet")
		case inCluster(st.Kubernetes) && st.Kubernetes.NodeReady != "True":
			last = fmt.Errorf("its Node is not Ready: %s", st.Kubernetes.NodeReady)
		default:
			r.say("%s: runs %s, found healthy", n.name, version)
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s: waiting for %s to be found healthy: %w", n.name, version, last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: waiting for %s to be found healthy: %w", n.name, version, context.Cause(ctx))
		case <-time.After(r.poll):
		}
	}
}

// otherBuild is the error of a node that runs the image's version, but not the image's store.
func (r *upgradeRun) otherBuild(n *upgradeNode, boot *nodev1.BootStatus) error {
	version := n.image.info.Version()
	if len(boot.GetRootHash()) == 0 {
		return fmt.Errorf("%s runs %s, but does not report the root hash of its store, so whether it runs this build of it is unknown", n.name, version)
	}
	return fmt.Errorf("%s runs another build of %s, whose store has the root hash %x, not %x; build the image with a new version", n.name, version, boot.GetRootHash(), n.image.header.RootHash)
}

// inCluster reports whether a node is part of a cluster, so its Node exists.
func inCluster(k *nodev1.KubernetesStatus) bool {
	return k != nil && (strings.HasPrefix(k.State, "bootstrapped") || strings.HasPrefix(k.State, "joined"))
}

// rolledBack is the error of a node that fell back from the image, with what its boots logged. A
// node of the cluster stays cordoned, for an operator to look at.
func (r *upgradeRun) rolledBack(n *upgradeNode, boot *nodev1.BootStatus) error {
	msg := fmt.Sprintf("%s: upgrade to %s failed: rolled back to %s", n.name, boot.Failed, boot.Version)
	if n.inCluster && !r.noReboot {
		msg += "; it stays cordoned"
	}
	if len(boot.Journal) > 0 {
		msg += "; its boots logged:\n  " + strings.Join(boot.Journal, "\n  ")
	}
	return errors.New(msg)
}
