package chalkd

import (
	"context"
	"log"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/vip"
	"github.com/trevex/chalkos/pkg/nodeconfig"
)

// Kubernetes is the node's Kubernetes side; nil on a node whose image has no Kubernetes.
type Kubernetes struct {
	Paths knode.Paths
	// Slot returns the running image's config slot on STATE, whose Addon documents the control
	// plane applies after chalkos's add-ons.
	Slot func() (string, error)
	// Objects returns the objects the control plane applies for the cluster: chalkos's add-ons,
	// then those of the Addon documents. Tests replace it.
	Objects func(c k8s.Cluster) ([]map[string]any, error)
	// ControlPlane runs on a bootstrapped control-plane node until ctx ends or it fails: it
	// applies the manifests, calls applied with their number once that succeeded, and approves
	// kubelet serving certificates. Tests replace it.
	ControlPlane func(ctx context.Context, share kpki.Share, applied func(n int)) error
	// Node returns the node's Node object. Tests replace it.
	Node func(ctx context.Context) (*corev1.Node, error)
	// RestartBackoff is the first wait before ControlPlane starts again after it failed; it
	// doubles up to MaxRestartBackoff. Zero means 5 seconds and a minute.
	RestartBackoff, MaxRestartBackoff time.Duration
	// EtcdEndpoints returns the client URLs of the cluster's etcd members other than the node's,
	// as the API server at the cluster's endpoint lists them; it fails while there is none.
	// Tests replace it.
	EtcdEndpoints func(ctx context.Context, c k8s.Cluster, share kpki.Share, self []netip.Addr) ([]string, error)
	// ClusterAnswers reports whether the API server at the cluster's endpoint answers with the
	// share's CA. Tests replace it.
	ClusterAnswers func(ctx context.Context, c k8s.Cluster, share kpki.Share) bool
	// JoinRetry is the wait between two attempts to join the cluster; zero means 10 seconds.
	JoinRetry time.Duration
	// PromoteTimeout is how long the join waits for its learner to catch up with the leader before
	// it starts over; zero means 10 minutes.
	PromoteTimeout time.Duration
	// VIPAddresses are the cluster's VIPs on this node's interfaces. Tests replace it.
	VIPAddresses func(c k8s.Cluster, p knode.Paths) (vip.Holder, error)
	// APIServerReady reports whether the node's API server answers ready. Tests replace it.
	APIServerReady func(ctx context.Context, c k8s.Cluster, share kpki.Share) bool
	// VIPTTL is the lifetime in seconds of the lease that holds the VIPs, and VIPInterval the
	// time between two health checks of the API server; zero means vip.DefaultTTL and
	// vip.DefaultInterval.
	VIPTTL      int
	VIPInterval time.Duration
	// EtcdTimeout bounds each request to etcd, so an etcd member that does not answer never holds
	// up a loop or the membership lock; zero means 30 seconds.
	EtcdTimeout time.Duration
	// EtcdStatusTimeout is how long a health check waits for each etcd member; zero means three
	// seconds. Tests raise it: their members share one busy process.
	EtcdStatusTimeout time.Duration
	// LocalEtcd is where chalkd reaches the node's own etcd member; empty means the cluster's
	// LocalEtcd. Tests replace it.
	LocalEtcd string
	// EtcdStopped waits until the node's etcd stopped once its static pod is gone. Tests replace
	// it.
	EtcdStopped func(ctx context.Context, c k8s.Cluster) error
	// ControlPlaneState says whether the node's API server and etcd run with the certificates the
	// node holds now: controlPlaneCurrent, or what is not so. Tests replace it.
	ControlPlaneState func(ctx context.Context, c k8s.Cluster, share kpki.Share) string
	// APIClient reaches the node's own API server in place of chalkd's client when set. Tests set
	// it.
	APIClient func(c k8s.Cluster, share kpki.Share) (kubernetes.Interface, error)
	// Approve approves kubelet serving certificates with the API server at cfg until ctx ends; nil
	// runs chalkd's approver. Tests replace it.
	Approve func(ctx context.Context, cfg *rest.Config)
	// RotationPoll is the time between two looks at what a rotation step waits for; zero means
	// two seconds.
	RotationPoll time.Duration
	// StatusTimeout bounds each read of the status from the API server, the revoked nodes and the
	// Node apart; zero means five seconds.
	StatusTimeout time.Duration

	// membership serialises the bootstrap and the join, which both make the node an etcd member.
	membership sync.Mutex

	// vipHolder is set while the node holds the VIPs.
	vipHolder atomic.Bool

	mu sync.Mutex
	// ctx ends the loops; cancel ends it when chalkd stops. vipDone is closed once the VIP
	// election ended and released the VIPs, joinDone once the join's loop ended, leavesDone
	// once the renewal of the control plane's certificates ended and controlPlaneDone once the
	// control plane's loop ended.
	ctx              context.Context
	cancel           context.CancelFunc
	vipDone          chan struct{}
	joinDone         chan struct{}
	leavesDone       chan struct{}
	controlPlaneDone chan struct{}
	// joining is set while the join's loop runs; joinStep is what it does or why it failed last,
	// and joinWaiting is set while it finds no cluster.
	joining     bool
	joinStep    string
	joinWaiting bool
	started     bool
	// stopped is set once chalkd stops; no loop starts after it.
	stopped bool
	// split says why the node's etcd and the other control planes' belong to different clusters.
	split string
	// pinProblem says why the node cannot be pinned to its addresses.
	pinProblem string
	// reload asks the running loop to start again.
	reload chan struct{}
	// done is closed once the manifests were applied; count is how many.
	done  chan struct{}
	count int
	// leaves renews the control plane's certificates while the control plane's loop runs.
	leaves *Renewal

	// apiMu guards api, chalkd's client for the API server; see ownAPIClient.
	apiMu sync.Mutex
	api   *apiConn
}

// LeavesProblem says why renewing the control plane's certificates failed last; "" while it
// did not.
func (k *Kubernetes) LeavesProblem() string {
	k.mu.Lock()
	leaves := k.leaves
	k.mu.Unlock()
	if leaves == nil {
		return ""
	}
	return leaves.Problem()
}

// NewKubernetes returns the Kubernetes side of a node with the default paths.
func NewKubernetes() *Kubernetes {
	k := &Kubernetes{Paths: knode.DefaultPaths(), Slot: nodeconfig.Default().Slot}
	k.Objects = k.objects
	k.ControlPlane = k.runControlPlane
	k.Node = k.node
	k.EtcdEndpoints = k.etcdEndpoints
	k.ClusterAnswers = clusterAnswers
	k.VIPAddresses = vip.NodeAddresses
	k.APIServerReady = apiServerReady
	k.EtcdStopped = etcdStopped
	k.ControlPlaneState = k.controlPlaneState
	return k
}

// loops returns the context the loops run in; k.mu is held.
func (k *Kubernetes) loops() context.Context {
	if k.ctx == nil {
		k.ctx, k.cancel = context.WithCancel(context.Background())
	}
	return k.ctx
}

// Stop ends the loops for good, as chalkd stops: a Start or Reload arriving meanwhile starts
// none again.
func (k *Kubernetes) Stop() {
	k.mu.Lock()
	k.stopped = true
	k.mu.Unlock()
	k.stopLoops()
}

// stopLoops ends the loops and waits until the node released the VIPs, so they never stay on a
// node that no longer holds their lease, until the join stopped, and until a renewal of the
// control plane's certificates, which renders the static pods, ended. Start starts them again.
func (k *Kubernetes) stopLoops() {
	k.mu.Lock()
	cancel, vipDone, joinDone, leavesDone, controlPlaneDone := k.cancel, k.vipDone, k.joinDone, k.leavesDone, k.controlPlaneDone
	k.ctx, k.cancel, k.vipDone, k.joinDone, k.leavesDone, k.controlPlaneDone = nil, nil, nil, nil, nil, nil
	// The next start runs the loops afresh and closes a channel of its own once they applied the
	// manifests.
	k.started, k.joining, k.done = false, false, nil
	k.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for what, done := range map[string]chan struct{}{"the VIP election": vipDone, "the join": joinDone, "the certificate renewal": leavesDone, "the control plane's loop": controlPlaneDone} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			log.Printf("kubernetes: %s did not end in time", what)
		}
	}
}

// Start runs the control plane's loop once, when the node is a bootstrapped control plane, and
// joins a control plane that is not to the cluster.
func (k *Kubernetes) Start() {
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		log.Printf("kubernetes: %v", err)
		return
	}
	m, leftovers, err := knode.ReadMembership(k.Paths)
	if err != nil {
		log.Printf("kubernetes: %v", err)
		return
	}
	if c.Kind != k8s.KindControlPlane {
		return
	}
	switch m {
	case knode.MembershipLeft:
		// A node that left etcd joins again only once reinstalled.
	case knode.MembershipBootstrapped:
		if leftovers.EtcdDataMissing {
			log.Printf("kubernetes: %v", knode.ErrEtcdDataMissing)
			return
		}
		k.start()
	default:
		k.startJoin()
	}
}

// start runs the control plane's loop and, when the cluster has VIPs, the VIP election unless
// they run already, and returns the channel closed once the manifests were applied.
func (k *Kubernetes) start() chan struct{} {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.startLocked()
}

// startLocked is start with k.mu held.
func (k *Kubernetes) startLocked() chan struct{} {
	if k.done == nil {
		k.done = make(chan struct{})
	}
	if k.started || k.stopped {
		return k.done
	}
	k.started = true
	k.reload = make(chan struct{}, 1)
	done := k.done
	ctx := k.loops()
	if c, err := k8s.ReadCluster(k.Paths.Cluster); err != nil {
		log.Printf("kubernetes: %v", err)
	} else if len(c.VIP.Addresses) > 0 {
		k.vipDone = make(chan struct{})
		go k.superviseVIP(ctx, k.vipDone)
	}
	if k.leaves == nil {
		k.leaves = k.controlPlaneRenewal()
	}
	k.leavesDone = make(chan struct{})
	go func(leaves *Renewal, done chan struct{}) {
		leaves.Run(ctx)
		close(done)
	}(k.leaves, k.leavesDone)
	var once sync.Once
	k.controlPlaneDone = make(chan struct{})
	go func(supervised chan struct{}) {
		defer close(supervised)
		k.superviseControlPlane(ctx, func(n int) {
			once.Do(func() {
				k.mu.Lock()
				k.count = n
				k.mu.Unlock()
				close(done)
			})
		}, k.reload)
	}(k.controlPlaneDone)
	return done
}

// Restart starts the loops again, as after the cluster's settings changed: the VIP election,
// the join and the control plane's loop read them when they start.
func (k *Kubernetes) Restart() {
	k.stopLoops()
	k.Start()
}

// Reload makes a running control-plane loop start again with the share and the config STATE
// holds now, which applies the control plane's objects again, or starts the loop when the node
// is a bootstrapped control plane.
func (k *Kubernetes) Reload() {
	k.mu.Lock()
	started, reload := k.started, k.reload
	k.mu.Unlock()
	if !started {
		k.Start()
		return
	}
	select {
	case reload <- struct{}{}:
	default:
		// A reload is pending already and reads the share when it happens.
	}
}

// etcdRequest bounds one request to etcd, and the health checks in it.
func (k *Kubernetes) etcdRequest(ctx context.Context) (context.Context, context.CancelFunc) {
	if k.EtcdStatusTimeout > 0 {
		ctx = etcd.WithStatusTimeout(ctx, k.EtcdStatusTimeout)
	}
	return context.WithTimeout(ctx, k.etcdTimeout())
}

func (k *Kubernetes) etcdTimeout() time.Duration {
	if k.EtcdTimeout <= 0 {
		return 30 * time.Second
	}
	return k.EtcdTimeout
}

// localEtcd is where chalkd reaches the node's own etcd member.
func (k *Kubernetes) localEtcd(c k8s.Cluster) string {
	if k.LocalEtcd != "" {
		return k.LocalEtcd
	}
	return c.LocalEtcd()
}

// statusTimeout bounds each read of the status from the API server.
func (k *Kubernetes) statusTimeout() time.Duration {
	if k.StatusTimeout > 0 {
		return k.StatusTimeout
	}
	return 5 * time.Second
}
