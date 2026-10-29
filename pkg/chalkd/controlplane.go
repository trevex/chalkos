package chalkd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"slices"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/trevex/chalkos/pkg/chalkd/renewal"
	"github.com/trevex/chalkos/pkg/config/slot"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/addons"
	kapply "github.com/trevex/chalkos/pkg/kubernetes/apply"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// controlPlaneRenewal renews the control plane's certificates.
func (k *Kubernetes) controlPlaneRenewal() *renewal.Renewal {
	return renewal.New("Kubernetes control-plane certificates", func(now time.Time) (bool, time.Time, error) {
		// Until the preparation wrote the certificates, as right after boot, there are none to
		// renew; those it writes are new.
		if prepared, err := knode.Prepared(k.Paths); err != nil || !prepared {
			return false, time.Time{}, err
		}
		renewAt, first, err := knode.ControlPlaneRenewAt(k.Paths)
		if errors.Is(err, fs.ErrNotExist) {
			return false, time.Time{}, nil
		}
		if err != nil {
			return false, time.Time{}, err
		}
		// Static pods left on the certificates before the current ones are rendered at once.
		stale, err := knode.StaticPodsStale(k.Paths)
		if err != nil {
			return false, first.NotAfter, err
		}
		return stale || !now.Before(renewAt), first.NotAfter, nil
	}, func(ctx context.Context, now time.Time) error {
		return knode.RenewControlPlane(ctx, k.Paths, now)
	})
}

// superviseControlPlane runs the control plane's loop and starts it again with backoff when it
// fails, and at once when Reload asks for it. Each start reads the share from STATE, so a share
// delivered since takes effect.
func (k *Kubernetes) superviseControlPlane(ctx context.Context, applied func(n int), reload <-chan struct{}) {
	first, limit := k.timing.RestartBackoff, k.timing.MaxRestartBackoff
	backoff := first
	for {
		started := time.Now()
		runCtx, cancel := context.WithCancel(ctx)
		result := make(chan error, 1)
		go func() { result <- k.runControlPlaneOnce(runCtx, applied) }()
		var err error
		reloaded := false
		select {
		case err = <-result:
		case <-reload:
			reloaded = true
			cancel()
			<-result
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		if reloaded {
			log.Print("kubernetes: the node's share or config changed; starting the control plane's loop again")
			backoff = first
			continue
		}
		if err == nil {
			return
		}
		// A loop that ran for a while failed afresh rather than again.
		if time.Since(started) > limit {
			backoff = first
		}
		log.Printf("kubernetes: the control plane's loop stopped: %v; starting it again in %v", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-reload:
			backoff = first
			continue
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, limit)
	}
}

func (k *Kubernetes) runControlPlaneOnce(ctx context.Context, applied func(n int)) error {
	if err := knode.CheckPrepared(k.Paths); err != nil {
		return err
	}
	// A renewal interrupted after it replaced the certificates left the static pods on the ones
	// before; unchanged manifests are not written, so nothing starts again.
	if err := knode.RefreshStaticPods(k.Paths); err != nil {
		log.Printf("kubernetes: render the static pods: %v", err)
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return err
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return err
	}
	if pin, err := knode.ReadPin(k.Paths); err != nil {
		return err
	} else if pin == nil {
		if err := k.pinFromEtcd(ctx, c, share); err != nil {
			return err
		}
	}
	go k.watchSplit(ctx, share)
	return k.hooks.ControlPlane(ctx, share, applied)
}

// pinFromEtcd pins a bootstrapped node without a pin, as an older image left it, to its
// addresses. It does so once its etcd member's peer URL confirms them, since etcd's peers know
// the member by that URL. A node whose address differs is not pinned, and its status says so.
func (k *Kubernetes) pinFromEtcd(ctx context.Context, c k8s.Cluster, share kpki.Share) error {
	ips, err := knode.ReadNodeIPs(k.Paths)
	if err != nil {
		return err
	}
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return err
	}
	defer cli.Close()
	rctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	self, err := etcd.Local(rctx, cli, k.localEtcd(c))
	if err != nil {
		return fmt.Errorf("pin the node's addresses: %w", err)
	}
	if !slices.Equal(self.PeerURLs, []string{k8s.EtcdPeerURL(ips[0])}) {
		problem := fmt.Sprintf("the node's address differs from its etcd peer URL %s; restore the address", strings.Join(self.PeerURLs, ", "))
		k.setPinProblem(problem)
		return errors.New(problem)
	}
	k.setPinProblem("")
	return knode.WritePin(k.Paths, ips)
}

func (k *Kubernetes) setPinProblem(problem string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.pinProblem = problem
}

// runControlPlane waits for the local API server, then approves kubelet serving certificates and
// applies the control plane's objects until that succeeds. chalkd's credential exists only in
// memory.
func (k *Kubernetes) runControlPlane(ctx context.Context, share kpki.Share, applied func(n int)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return err
	}
	cred := newCredential(share, credentialValidity, time.Now)
	// Issuing once up front fails the loop early on a share that cannot issue.
	if _, err := cred.GetClientCertificate(nil); err != nil {
		return fmt.Errorf("issue chalkd's client certificate: %w", err)
	}
	go cred.renew(ctx)
	cfg, err := cred.restConfig(c.LocalAPIServer())
	if err != nil {
		return err
	}
	return k.serveControlPlane(ctx, c, cfg, applied)
}

// serveControlPlane runs until ctx ends. It does two things independently:
//   - approves kubelet serving certificates once the API server is ready;
//   - applies the control plane's objects until that succeeds.
//
// Objects that fail, such as an Addon document's, so leave kubelets their serving certificates.
func (k *Kubernetes) serveControlPlane(ctx context.Context, c k8s.Cluster, cfg *rest.Config, applied func(n int)) error {
	go func() {
		err := kapply.WaitReady(ctx, cfg)
		switch {
		case err == nil:
			k.hooks.Approve(ctx, cfg)
		case ctx.Err() == nil:
			log.Printf("kubernetes: approve kubelet serving certificates: %v", err)
		}
	}()
	for {
		n, err := k.applyOnce(ctx, c, cfg)
		if err == nil {
			log.Printf("kubernetes: applied %s", count(n, "object"))
			applied(n)
			break
		}
		log.Printf("kubernetes: %v; retrying", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

// approveServing approves kubelet serving certificates until ctx ends.
func approveServing(ctx context.Context, cfg *rest.Config) {
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Printf("kubernetes: approve kubelet serving certificates: %v", err)
		return
	}
	(&kapply.Approver{Client: client}).Run(ctx, 10*time.Second)
}

func (k *Kubernetes) applyOnce(ctx context.Context, c k8s.Cluster, cfg *rest.Config) (int, error) {
	if err := kapply.WaitReady(ctx, cfg); err != nil {
		return 0, err
	}
	// etcd holds the cluster's data now; from here on an empty data directory is a loss.
	if err := knode.MarkEtcdInitialised(k.Paths, time.Now()); err != nil {
		return 0, fmt.Errorf("record that etcd is initialised: %w", err)
	}
	objects, err := k.hooks.Objects(c)
	if err != nil {
		return 0, err
	}
	// The revoked nodes' admission policy is not among the objects: the approver applies it from
	// the list as it changes and retries it apart, so a policy that fails holds back no add-on.
	a, err := kapply.NewApplier(cfg)
	if err != nil {
		return 0, err
	}
	if err := a.Apply(ctx, objects); err != nil {
		return 0, err
	}
	return len(objects), nil
}

// objects are chalkos's add-ons for the cluster, then the objects of the node's Addon documents.
func (k *Kubernetes) objects(c k8s.Cluster) ([]map[string]any, error) {
	objects, err := addons.Render(c)
	if err != nil {
		return nil, fmt.Errorf("render chalkos's add-ons: %w", err)
	}
	dir, err := k.hooks.Slot()
	if err != nil {
		return nil, fmt.Errorf("find the running image's config slot: %w", err)
	}
	stored, _, err := slot.Read(dir)
	if err != nil {
		return nil, fmt.Errorf("read the stored config: %w", err)
	}
	extra, err := stored.Documents.AddonObjects()
	if err != nil {
		return nil, err
	}
	return append(objects, extra...), nil
}

// splitInterval is the time between two comparisons of the node's etcd cluster with the other
// control planes'.
const splitInterval = time.Minute

// watchSplit compares the cluster of the node's etcd member with the ones of the other control
// planes' members until ctx ends, and reports in the node's status when they differ. It changes
// nothing: which cluster's data to keep is the operator's decision.
func (k *Kubernetes) watchSplit(ctx context.Context, share kpki.Share) {
	for {
		split, err := k.checkSplit(ctx, share)
		// A check that could not tell keeps what the last one found.
		if err == nil {
			msg := ""
			if split != nil {
				msg = split.Error()
			}
			k.mu.Lock()
			changed := k.split != msg
			k.split = msg
			k.mu.Unlock()
			if changed && msg != "" {
				log.Printf("kubernetes: %s", msg)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(splitInterval):
		}
	}
}

// checkSplit asks the node's etcd member and the other control planes' which cluster they belong
// to; the split is nil when they agree.
func (k *Kubernetes) checkSplit(ctx context.Context, share kpki.Share) (*etcd.SplitError, error) {
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return nil, err
	}
	ips, err := knode.ReadNodeIPs(k.Paths)
	if err != nil {
		return nil, err
	}
	others, err := k.hooks.EtcdEndpoints(ctx, c, share, ips)
	if err != nil {
		return nil, err
	}
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	rctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	answered, err := etcd.OneCluster(rctx, cli, append([]string{k.localEtcd(c)}, others...))
	if split := (*etcd.SplitError)(nil); errors.As(err, &split) {
		return split, nil
	}
	if err != nil {
		return nil, err
	}
	if answered[0] != k.localEtcd(c) {
		return nil, errors.New("the node's etcd member did not answer")
	}
	return nil, nil
}
