package chalkd

import (
	"cmp"
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
	Paths  knode.Paths
	hooks  KubernetesHooks
	timing Timing
	// etcdURL is where chalkd reaches the node's own etcd member; empty means the cluster's
	// LocalEtcd.
	etcdURL string

	// membership serialises the bootstrap and the join, which both make the node an etcd member.
	membership sync.Mutex

	// vipHolder is set while the node holds the VIPs.
	vipHolder atomic.Bool

	mu sync.Mutex
	// ctx ends the loops, and cancel ends ctx when chalkd stops. Each done channel is closed once
	// its loop ended:
	//   - vipDone: the VIP election, which released the VIPs by then;
	//   - joinDone: the join;
	//   - leavesDone: the renewal of the control plane's certificates;
	//   - controlPlaneDone: the control plane's loop.
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

// KubernetesHooks are the functions through which the Kubernetes side reaches the cluster, the
// node and STATE. NewKubernetes fills each nil one with chalkd's own; tests replace them.
type KubernetesHooks struct {
	// Slot returns the running image's config slot on STATE. The control plane applies the slot's
	// Addon documents after chalkos's add-ons.
	Slot func() (string, error)
	// Objects returns the objects the control plane applies for the cluster: chalkos's add-ons,
	// then those of the Addon documents.
	Objects func(c k8s.Cluster) ([]map[string]any, error)
	// ControlPlane runs on a bootstrapped control-plane node until ctx ends or it fails. It
	// applies the manifests, calls applied with their number once that succeeded, and approves
	// kubelet serving certificates.
	ControlPlane func(ctx context.Context, share kpki.Share, applied func(n int)) error
	// Node returns the node's Node object.
	Node func(ctx context.Context) (*corev1.Node, error)
	// EtcdEndpoints returns the client URLs of the cluster's etcd members other than the node's,
	// as the API server at the cluster's endpoint lists them. It fails while there is none.
	EtcdEndpoints func(ctx context.Context, c k8s.Cluster, share kpki.Share, self []netip.Addr) ([]string, error)
	// ClusterAnswers reports whether the API server at the cluster's endpoint answers with the
	// share's CA.
	ClusterAnswers func(ctx context.Context, c k8s.Cluster, share kpki.Share) bool
	// VIPAddresses are the cluster's VIPs on this node's interfaces.
	VIPAddresses func(c k8s.Cluster, p knode.Paths) (vip.Holder, error)
	// APIServerReady reports whether the node's API server answers ready.
	APIServerReady func(ctx context.Context, c k8s.Cluster, share kpki.Share) bool
	// EtcdStopped waits until the node's etcd stopped once its static pod is gone.
	EtcdStopped func(ctx context.Context, c k8s.Cluster) error
	// ControlPlaneState says whether the node's API server and etcd run with the certificates the
	// node holds now: controlPlaneCurrent, or what is not so.
	ControlPlaneState func(ctx context.Context, c k8s.Cluster, share kpki.Share) string
	// APIClient returns a client for the node's own API server.
	APIClient func(c k8s.Cluster, share kpki.Share) (kubernetes.Interface, error)
	// Approve approves kubelet serving certificates with the API server at cfg until ctx ends.
	Approve func(ctx context.Context, cfg *rest.Config)
}

// Timing holds the waits and timeouts of the Kubernetes side. A zero field means its default.
type Timing struct {
	// RestartBackoff is the first wait before the control plane's loop starts again after it
	// failed; it doubles up to MaxRestartBackoff. The defaults are 5 seconds and a minute.
	RestartBackoff, MaxRestartBackoff time.Duration
	// JoinRetry is the wait between two attempts to join the cluster; 10 seconds by default.
	JoinRetry time.Duration
	// PromoteTimeout is how long the join waits for its learner to catch up with the leader
	// before it starts over; 10 minutes by default.
	PromoteTimeout time.Duration
	// VIPTTL is the lifetime in seconds of the lease that holds the VIPs, and VIPInterval the
	// time between two health checks of the API server. The defaults are vip.DefaultTTL and
	// vip.DefaultInterval.
	VIPTTL      int
	VIPInterval time.Duration
	// EtcdTimeout bounds each request to etcd, so an etcd member that does not answer never holds
	// up a loop or the membership lock; 30 seconds by default.
	EtcdTimeout time.Duration
	// EtcdStatusTimeout is how long a health check waits for each etcd member. Zero leaves etcd's
	// own three seconds. Tests raise it: their members share one busy process.
	EtcdStatusTimeout time.Duration
	// Poll is the time between two looks at what a rotation step or a drain waits for; two
	// seconds by default.
	Poll time.Duration
	// StatusTimeout bounds each read of the status from the API server, the revoked nodes and the
	// Node apart; five seconds by default.
	StatusTimeout time.Duration
}

// withDefaults returns t with each zero field but EtcdStatusTimeout set to its default.
func (t Timing) withDefaults() Timing {
	t.RestartBackoff = cmp.Or(t.RestartBackoff, 5*time.Second)
	t.MaxRestartBackoff = cmp.Or(t.MaxRestartBackoff, time.Minute)
	t.JoinRetry = cmp.Or(t.JoinRetry, 10*time.Second)
	t.PromoteTimeout = cmp.Or(t.PromoteTimeout, 10*time.Minute)
	t.VIPTTL = cmp.Or(t.VIPTTL, vip.DefaultTTL)
	t.VIPInterval = cmp.Or(t.VIPInterval, vip.DefaultInterval)
	t.EtcdTimeout = cmp.Or(t.EtcdTimeout, 30*time.Second)
	t.Poll = cmp.Or(t.Poll, 2*time.Second)
	t.StatusTimeout = cmp.Or(t.StatusTimeout, 5*time.Second)
	return t
}

// KubernetesOptions set up the Kubernetes side NewKubernetes returns. Zero fields mean chalkd's
// own: the node's paths, chalkd's hooks and the default timing.
type KubernetesOptions struct {
	Paths  knode.Paths
	Hooks  KubernetesHooks
	Timing Timing
	// LocalEtcd is where chalkd reaches the node's own etcd member; empty means the cluster's
	// LocalEtcd.
	LocalEtcd string
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

// NewKubernetes returns the Kubernetes side of a node.
func NewKubernetes(o KubernetesOptions) *Kubernetes {
	k := &Kubernetes{Paths: o.Paths, hooks: o.Hooks, timing: o.Timing.withDefaults(), etcdURL: o.LocalEtcd}
	if k.Paths == (knode.Paths{}) {
		k.Paths = knode.DefaultPaths()
	}
	h := &k.hooks
	if h.Slot == nil {
		h.Slot = nodeconfig.Default().Slot
	}
	if h.Objects == nil {
		h.Objects = k.objects
	}
	if h.ControlPlane == nil {
		h.ControlPlane = k.runControlPlane
	}
	if h.Node == nil {
		h.Node = k.node
	}
	if h.EtcdEndpoints == nil {
		h.EtcdEndpoints = k.etcdEndpoints
	}
	if h.ClusterAnswers == nil {
		h.ClusterAnswers = clusterAnswers
	}
	if h.VIPAddresses == nil {
		h.VIPAddresses = vip.NodeAddresses
	}
	if h.APIServerReady == nil {
		h.APIServerReady = apiServerReady
	}
	if h.EtcdStopped == nil {
		h.EtcdStopped = etcdStopped
	}
	if h.ControlPlaneState == nil {
		h.ControlPlaneState = k.controlPlaneState
	}
	if h.APIClient == nil {
		h.APIClient = k.ownAPIClient
	}
	if h.Approve == nil {
		h.Approve = approveServing
	}
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
	if k.timing.EtcdStatusTimeout > 0 {
		ctx = etcd.WithStatusTimeout(ctx, k.timing.EtcdStatusTimeout)
	}
	return context.WithTimeout(ctx, k.timing.EtcdTimeout)
}

// localEtcd is where chalkd reaches the node's own etcd member.
func (k *Kubernetes) localEtcd(c k8s.Cluster) string {
	return cmp.Or(k.etcdURL, c.LocalEtcd())
}
