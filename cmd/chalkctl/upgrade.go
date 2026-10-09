package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/manifest"
)

const upgradeHelp = `usage: chalkctl upgrade --image PATH [--nodes N,...] [--max-unavailable N] [--allow-downtime] [--no-reboot] [--retry-failed] [--delete-emptydir-data] [flags]

Installs an image on the nodes of its role: control planes one at a time, each only while etcd
keeps its quorum without it, then workers and nodes without Kubernetes in batches of
--max-unavailable. A node gets the image in its inactive slot, is cordoned and drained within
its pods' PodDisruptionBudgets, reboots into the image, and is uncordoned once the boot was found
healthy and the node is Ready. A node whose image never becomes healthy falls back to the image
before, and the run stops there, showing what that boot logged; with --retry-failed a node that
fell back from the image before gets it once more.

Run again, the command skips nodes that run the image and continues one it stopped at; it
uncordons only nodes it cordoned itself. etcd of one or two control planes loses its quorum while
one reboots, and the API server is down, which --allow-downtime accepts. Pods with emptyDir
volumes are evicted only with --delete-emptydir-data. An operator client file is enough to run it.

flags:
`

// upgradeRun upgrades nodes to an image.
type upgradeRun struct {
	a       *app
	cluster *cluster
	creds   *credentials
	image   *upgradeImage
	// endpoints are the nodes' chalkd addresses that --endpoint gives.
	endpoints map[string]string
	// nodes are the nodes to upgrade, as they were when the run started.
	nodes map[string]*upgradeNode
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
// of a cluster, so its Node exists.
type upgradeNode struct {
	name, kind string
	inCluster  bool
}

func (a *app) upgrade(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), upgradeHelp)
		fs.PrintDefaults()
	}
	var cf clusterFlags
	var sf secretFlags
	cf.register(fs)
	sf.register(fs)
	config := fs.String("config", "", "client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, when there is no secrets file)")
	endpoints := endpointList{}
	fs.Var(endpoints, "endpoint", "address of a node's chalkd, NODE=ADDR, host or host:port; may be repeated (default each node's first static address)")
	imagePath := fs.String("image", "", "role image to install: a raw image with repart-output.json next to it, or the directory nix build produces")
	signKey := fs.String("sign-key", "", "PEM key of the Secure Boot db signer, to sign the image's UKI")
	signCert := fs.String("sign-cert", "", "PEM certificate of the Secure Boot db signer")
	nodesFlag := fs.String("nodes", "", "comma-separated nodes to upgrade (default every node of the image's role)")
	maxUnavailable := fs.Int("max-unavailable", 1, "how many workers and nodes without Kubernetes upgrade at once")
	allowDowntime := fs.Bool("allow-downtime", false, "upgrade one or two control planes, whose etcd loses its quorum and API server is down while one reboots")
	noReboot := fs.Bool("no-reboot", false, "install the image on the nodes without draining or rebooting them; it boots with their next reboot")
	retryFailed := fs.Bool("retry-failed", false, "install the image again, once, on nodes that fell back from it")
	deleteEmptyDir := fs.Bool("delete-emptydir-data", false, "evict pods with emptyDir volumes too, deleting their data")
	timeout := fs.Duration("timeout", 30*time.Minute, "how long to wait for each node to come back healthy and for its drain")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *imagePath == "" || *maxUnavailable < 1 {
		return errors.New("usage: chalkctl upgrade --image PATH [--nodes N,...] [--max-unavailable N] [--allow-downtime] [--no-reboot] [--retry-failed] [--delete-emptydir-data]")
	}
	creds, err := a.loadCredentials(ctx, sf, *config, cf.flake)
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
	img, err := openUpgradeImage(ctx, *imagePath, *signKey, *signCert)
	if err != nil {
		return err
	}
	defer img.Close()
	if err := checkImage(c, img, *signCert); err != nil {
		return err
	}
	r := &upgradeRun{
		a: a, cluster: c, creds: creds, image: img, endpoints: endpoints,
		allowDowntime: *allowDowntime, noReboot: *noReboot, maxUnavailable: *maxUnavailable,
		retryFailed: *retryFailed, deleteEmptyDir: *deleteEmptyDir,
		timeout: *timeout, drainTimeout: *timeout, poll: a.poll(), quorumWait: min(*timeout, time.Minute),
	}
	var named []string
	if *nodesFlag != "" {
		named = strings.Split(*nodesFlag, ",")
	}
	if err := r.find(ctx, named); err != nil {
		return err
	}
	return r.run(ctx)
}

// checkImage checks the image against the cluster: its cluster, and the signature of its UKI
// when the cluster names a db certificate, or else when chalkctl signed it.
func checkImage(c *cluster, img *upgradeImage, signCert string) error {
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

// find picks the nodes to upgrade: those named, which must run the image's role, or every node of
// the role. A client file without a cluster definition knows no roles, so its nodes are asked.
func (r *upgradeRun) find(ctx context.Context, named []string) error {
	m := r.cluster.manifest
	role := r.image.info.Role()
	candidates := named
	if len(candidates) == 0 {
		for _, name := range slices.Sorted(maps.Keys(m.Nodes)) {
			if r.cluster.partial || m.Nodes[name].Role == role {
				candidates = append(candidates, name)
			}
		}
	}
	r.nodes = map[string]*upgradeNode{}
	// The control planes a client file names are known by asking them.
	probedCPs := map[string]bool{}
	for _, name := range candidates {
		node, err := r.cluster.node(name)
		if err != nil {
			return err
		}
		if !r.cluster.partial && node.Role != role {
			return fmt.Errorf("%s is a node of the role %s; the image is of %s", name, node.Role, role)
		}
		info, st, err := r.probe(ctx, name)
		if err != nil {
			return err
		}
		switch {
		case info.Cluster != r.image.info.Cluster():
			return fmt.Errorf("%s runs an image of the cluster %q, not %s", name, info.Cluster, r.image.info.Cluster())
		case info.Role != role && len(named) > 0:
			return fmt.Errorf("%s runs an image of the role %q; the image is of %s", name, info.Role, role)
		}
		n := &upgradeNode{name: name}
		if k := st.Kubernetes; k != nil {
			n.kind, n.inCluster = k.Kind, inCluster(k)
		}
		probedCPs[name] = n.kind == manifest.KindControlPlane
		if info.Role == role {
			r.nodes[name] = n
		}
	}
	if len(r.nodes) == 0 {
		return fmt.Errorf("the cluster has no node of the role %s to upgrade", role)
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
	r.say("upgrading %s to %s %s: %s", r.image.info.Role(), r.image.info.ID(), r.image.info.Version(), strings.Join(slices.Concat(controlPlanes, others), ", "))
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
	if r.noReboot {
		r.say("installed %s on %d nodes; it boots with their next reboot", r.image.info.Version(), len(r.nodes))
	} else {
		r.say("upgraded %d nodes to %s", len(r.nodes), r.image.info.Version())
	}
	return nil
}

// node upgrades one node, continuing where an earlier run stopped: a node that runs the image
// and was found healthy is uncordoned once its Node is Ready, one that booted it is waited for,
// and the image installed already is not sent again.
func (r *upgradeRun) node(ctx context.Context, n *upgradeNode) error {
	version := r.image.info.Version()
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
	case runs && !bytes.Equal(boot.GetRootHash(), r.image.header.RootHash):
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
		} else if err := r.install(ctx, n.name); err != nil {
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

// install sends the node the image.
func (r *upgradeRun) install(ctx context.Context, name string) error {
	r.say("%s: installing %s", name, r.image.info.Version())
	parts, done, err := r.image.parts()
	if err != nil {
		return err
	}
	defer done()
	var resp *nodev1.UpgradeResponse
	err = r.call(name, func(conn *client.Conn) error {
		stream := conn.Upgrade(ctx)
		if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: r.image.header}}); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		buf := make([]byte, chunkSize)
		for {
			n, rerr := io.ReadFull(parts, buf)
			if n > 0 {
				msg := &nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Chunk{Chunk: &nodev1.ImageChunk{Data: buf[:n]}}}
				// The node may have refused already; CloseAndReceive returns why.
				if err := stream.Send(msg); errors.Is(err, io.EOF) {
					break
				} else if err != nil {
					return err
				}
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			if rerr != nil {
				return rerr
			}
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
		r.say("%s: runs %s already", name, r.image.info.Version())
	} else {
		r.say("%s: installed %s, which boots next as %s", name, r.image.info.Version(), resp.Entry)
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
		return finalError{fmt.Errorf("etcd has %d voters, and without %s fewer than the %d its quorum needs: it and the API server are down while %s reboots; pass --allow-downtime to accept that", voters, name, quorum, name)}
	case voters-1 < quorum:
		return nil
	case healthy < quorum:
		return fmt.Errorf("without %s etcd has %d healthy voters of %d, fewer than the %d its quorum needs", name, healthy, voters, quorum)
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
	r.say("%s: %s, evicted %d pods, kept %d", n.name, cordon, len(resp.Evicted), len(resp.Kept)+len(resp.Unmanaged))
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
	version := r.image.info.Version()
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
		case !bytes.Equal(st.GetBoot().GetRootHash(), r.image.header.RootHash):
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
	version := r.image.info.Version()
	if len(boot.GetRootHash()) == 0 {
		return fmt.Errorf("%s runs %s, but does not report the root hash of its store, so whether it runs this build of it is unknown", n.name, version)
	}
	return fmt.Errorf("%s runs another build of %s, whose store has the root hash %x, not %x; build the image with a new version", n.name, version, boot.GetRootHash(), r.image.header.RootHash)
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
