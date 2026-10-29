package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

var _ layer.Health = (*Plugin)(nil)

// Health checks the node's part in its cluster once it holds a share: a worker's kubelet is
// healthy and its Node registered; a bootstrapped control plane's etcd member answers, its API
// server is ready and its kubelet healthy. A node without a share, and a control plane that is no
// etcd member yet, leave the decision to the common checks.
func (p *Plugin) Health(ctx context.Context) (bool, error) {
	k := p.k
	if _, err := os.Stat(k.paths.Share()); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	switch prep, err := readPreparation(k.paths); {
	case err != nil:
		return true, err
	case prep.problem() != "":
		return true, errors.New(prep.problem())
	}
	c, err := k8s.ReadCluster(k.paths.Cluster)
	if err != nil {
		return true, err
	}
	if c.Kind == k8s.KindWorker {
		if err := p.kubeletHealthy(ctx); err != nil {
			return true, err
		}
		if _, err := k.hooks.Node(ctx); err != nil {
			return true, fmt.Errorf("the Node is not registered: %w", err)
		}
		return true, nil
	}
	// A control plane that is no etcd member yet waits for a bootstrap or a cluster to join.
	if m, _, err := knode.ReadMembership(k.paths); err != nil {
		return true, err
	} else if m != knode.MembershipBootstrapped {
		return false, nil
	}
	share, err := knode.ReadShare(k.paths)
	if err != nil {
		return true, err
	}
	if err := p.etcdHealthy(ctx, c, share); err != nil {
		return true, err
	}
	if !k.hooks.APIServerReady(ctx, c, share) {
		return true, errors.New("the API server is not ready")
	}
	return true, p.kubeletHealthy(ctx)
}

func (p *Plugin) kubeletHealthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.k.kubelet, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("the kubelet is not healthy: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the kubelet is not healthy: %s", resp.Status)
	}
	return nil
}

// etcdHealthy checks that the node's own etcd member answers.
func (p *Plugin) etcdHealthy(ctx context.Context, c k8s.Cluster, share kpki.Share) error {
	k := p.k
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return fmt.Errorf("etcd: %w", err)
	}
	defer cli.Close()
	ctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	own, err := etcd.Local(ctx, cli, k.localEtcd(c))
	if err != nil {
		return fmt.Errorf("the etcd member is not healthy: %w", err)
	}
	if err := etcd.Health(ctx, cli, []etcd.Member{own})[own.ID]; err != nil {
		return fmt.Errorf("the etcd member is not healthy: %w", err)
	}
	return nil
}
