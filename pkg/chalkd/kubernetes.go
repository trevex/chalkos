package chalkd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kapply "github.com/trevex/chalkos/pkg/kubernetes/apply"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// Kubernetes is the node's Kubernetes side; nil on a node of a role without Kubernetes.
type Kubernetes struct {
	Paths knode.Paths
	// Manifests is the image's list of objects the control plane applies.
	Manifests string
	// ControlPlane runs on a bootstrapped control-plane node until ctx ends or it fails: it
	// applies the manifests, calls applied with their number once that succeeded, and approves
	// kubelet serving certificates. Tests replace it.
	ControlPlane func(ctx context.Context, share kpki.Share, applied func(n int)) error
	// NodeReady returns the status of the node's Ready condition.
	NodeReady func(ctx context.Context) (string, error)
	// RestartBackoff is the first wait before ControlPlane starts again after it failed; it
	// doubles up to MaxRestartBackoff. Zero means 5 seconds and a minute.
	RestartBackoff, MaxRestartBackoff time.Duration
	// EtcdEndpoints returns the client URLs of the cluster's etcd members other than the node's,
	// as the API server at the cluster's endpoint lists them; it fails while there is none.
	// Tests replace it.
	EtcdEndpoints func(ctx context.Context, c k8s.Cluster, share kpki.Share, self []net.IP) ([]string, error)
	// ClusterAnswers reports whether the API server at the cluster's endpoint answers with the
	// share's CA. Tests replace it.
	ClusterAnswers func(ctx context.Context, c k8s.Cluster, share kpki.Share) bool
	// JoinRetry is the wait between two attempts to join the cluster; zero means 10 seconds.
	JoinRetry time.Duration
	// PromoteTimeout is how long the join waits for its learner to catch up with the leader before
	// it starts over; zero means 10 minutes.
	PromoteTimeout time.Duration
	// VIPAddresses are the cluster's VIPs on this node's interfaces. Tests replace it.
	VIPAddresses func(c k8s.Cluster, p knode.Paths) (AddressManager, error)
	// APIServerReady reports whether the node's API server answers ready. Tests replace it.
	APIServerReady func(ctx context.Context, share kpki.Share) bool
	// VIPTTL is the lifetime in seconds of the lease that holds the VIPs, and VIPInterval the
	// time between two health checks of the API server; zero means 10 seconds and 2 seconds.
	VIPTTL      int
	VIPInterval time.Duration
	// EtcdTimeout bounds each request to etcd, so an etcd member that does not answer never holds
	// up a loop or the membership lock; zero means 30 seconds.
	EtcdTimeout time.Duration
	// LocalEtcd is where chalkd reaches the node's own etcd member; empty means
	// https://127.0.0.1:2379. Tests replace it.
	LocalEtcd string
	// EtcdStopped waits until the node's etcd stopped once its static pod is gone. Tests replace
	// it.
	EtcdStopped func(ctx context.Context) error

	// membership serialises the bootstrap and the join, which both make the node an etcd member.
	membership sync.Mutex

	// vipHolder is set while the node holds the VIPs.
	vipHolder atomic.Bool

	mu sync.Mutex
	// ctx ends the loops; cancel ends it when chalkd stops. vipDone is closed once the VIP
	// election ended and released the VIPs, and joinDone once the join's loop ended.
	ctx      context.Context
	cancel   context.CancelFunc
	vipDone  chan struct{}
	joinDone chan struct{}
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
}

// NewKubernetes returns the Kubernetes side of a node with the default paths.
func NewKubernetes() *Kubernetes {
	k := &Kubernetes{Paths: knode.DefaultPaths(), Manifests: "/etc/chalkos/kubernetes/manifests.json"}
	k.ControlPlane = k.runControlPlane
	k.NodeReady = k.nodeReady
	k.EtcdEndpoints = k.etcdEndpoints
	k.ClusterAnswers = clusterAnswers
	k.VIPAddresses = systemVIPAddresses
	k.APIServerReady = apiServerReady
	k.EtcdStopped = etcdStopped
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
// node that no longer holds their lease, and until the join stopped. Start starts them again.
func (k *Kubernetes) stopLoops() {
	k.mu.Lock()
	cancel, vipDone, joinDone := k.cancel, k.vipDone, k.joinDone
	k.ctx, k.cancel, k.vipDone, k.joinDone = nil, nil, nil, nil
	// The next start runs the loops afresh and closes a channel of its own once they applied the
	// manifests.
	k.started, k.joining, k.done = false, false, nil
	k.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for what, done := range map[string]chan struct{}{"the VIP election": vipDone, "the join": joinDone} {
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
	bootstrapped, err := knode.Bootstrapped(k.Paths)
	if err != nil {
		log.Printf("kubernetes: %v", err)
		return
	}
	if c.Kind != k8s.KindControlPlane {
		return
	}
	// A node that left etcd joins again only once reinstalled.
	if left, err := knode.Left(k.Paths); err != nil || left {
		return
	}
	if !bootstrapped {
		k.startJoin()
		return
	}
	if err := knode.CheckEtcdData(k.Paths); err != nil {
		log.Printf("kubernetes: %v", err)
		return
	}
	k.start()
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
	var once sync.Once
	go k.superviseControlPlane(ctx, func(n int) {
		once.Do(func() {
			k.mu.Lock()
			k.count = n
			k.mu.Unlock()
			close(done)
		})
	}, k.reload)
	return done
}

// Reload makes a running control-plane loop start again with the share STATE holds now, or
// starts the loop when the node is a bootstrapped control plane.
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

// superviseControlPlane runs the control plane's loop and starts it again with backoff when it
// fails, and at once when Reload asks for it. Each start reads the share from STATE, so a share
// delivered since takes effect.
func (k *Kubernetes) superviseControlPlane(ctx context.Context, applied func(n int), reload <-chan struct{}) {
	first, limit := k.RestartBackoff, k.MaxRestartBackoff
	if first <= 0 {
		first = 5 * time.Second
	}
	if limit <= 0 {
		limit = time.Minute
	}
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
			log.Print("kubernetes: the node's share changed; starting the control plane's loop again")
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

// errNotPrepared stops the control plane's loop until the preparation finished; its backoff
// tries again.
var errNotPrepared = errors.New("the node's Kubernetes files are not prepared yet")

func (k *Kubernetes) runControlPlaneOnce(ctx context.Context, applied func(n int)) error {
	if prepared, err := knode.Prepared(k.Paths); err != nil {
		return err
	} else if !prepared {
		return errNotPrepared
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return err
	}
	if pin, err := knode.ReadPin(k.Paths); err != nil {
		return err
	} else if pin == nil {
		if err := k.pinFromEtcd(ctx, share); err != nil {
			return err
		}
	}
	go k.watchSplit(ctx, share)
	return k.ControlPlane(ctx, share, applied)
}

// pinFromEtcd pins a bootstrapped node without a pin, as an older image left it, to its
// addresses once its etcd member's peer URL confirms them: etcd's peers know the member by that
// URL. A node whose address differs is not pinned, and its status says so.
func (k *Kubernetes) pinFromEtcd(ctx context.Context, share kpki.Share) error {
	ips, err := knode.ReadNodeIPs(k.Paths)
	if err != nil {
		return err
	}
	cli, err := k.dialEtcd(share)
	if err != nil {
		return err
	}
	defer cli.Close()
	rctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	self, err := etcd.Local(rctx, cli, k.localEtcd())
	if err != nil {
		return fmt.Errorf("pin the node's addresses: %w", err)
	}
	if !slices.Equal(self.PeerURLs, []string{etcd.PeerURL(ips[0])}) {
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

// runControlPlane waits for the local API server, applies the manifests until that succeeds,
// and then approves kubelet serving certificates. chalkd's credential exists only in memory.
func (k *Kubernetes) runControlPlane(ctx context.Context, share kpki.Share, applied func(n int)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cred := newCredential(share, credentialValidity, time.Now)
	// Issuing once up front fails the loop early on a share that cannot issue.
	if _, err := cred.GetClientCertificate(nil); err != nil {
		return fmt.Errorf("issue chalkd's client certificate: %w", err)
	}
	go cred.renew(ctx)
	cfg, err := cred.restConfig(kpki.LocalAPIServer)
	if err != nil {
		return err
	}
	for {
		n, err := k.applyOnce(ctx, cfg)
		if err == nil {
			log.Printf("kubernetes: applied %d objects", n)
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
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	(&kapply.Approver{Client: client}).Run(ctx, 10*time.Second)
	return ctx.Err()
}

func (k *Kubernetes) applyOnce(ctx context.Context, cfg *rest.Config) (int, error) {
	if err := kapply.WaitReady(ctx, cfg); err != nil {
		return 0, err
	}
	// etcd holds the cluster's data now; from here on an empty data directory is a loss.
	if err := knode.MarkEtcdInitialised(k.Paths, time.Now()); err != nil {
		return 0, fmt.Errorf("record that etcd is initialised: %w", err)
	}
	objects, err := kapply.ReadManifests(k.Manifests)
	if err != nil {
		return 0, err
	}
	a, err := kapply.NewApplier(cfg)
	if err != nil {
		return 0, err
	}
	if err := a.Apply(ctx, objects); err != nil {
		return 0, err
	}
	return len(objects), nil
}

// nodeReady reads the node's Node object with the kubelet's credentials, which may read their
// own node.
func (k *Kubernetes) nodeReady(ctx context.Context) (string, error) {
	n, err := k8s.ReadNode(k.Paths.NodeFile)
	if err != nil {
		return "", err
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", k.Paths.Kubeconfig())
	if err != nil {
		return "", err
	}
	cfg.Timeout = 5 * time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	node, err := client.CoreV1().Nodes().Get(ctx, n.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return string(cond.Status), nil
		}
	}
	return string(corev1.ConditionUnknown), nil
}

func (s *Server) Bootstrap(ctx context.Context, _ *connect.Request[nodev1.BootstrapRequest]) (*connect.Response[nodev1.BootstrapResponse], error) {
	k := s.Kubernetes
	if k == nil {
		return nil, failed(connect.CodeFailedPrecondition, "the node's role has no Kubernetes")
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if c.Kind != k8s.KindControlPlane {
		return nil, failed(connect.CodeFailedPrecondition, "the node is a worker; bootstrap a control-plane node")
	}
	s.mu.Lock()
	done, err := s.bootstrap(ctx, k, c)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, failed(connect.CodeDeadlineExceeded, "etcd is initialised and the control plane starts, but its manifests were not applied yet; chalkctl status shows its progress")
	}
	k.mu.Lock()
	n := k.count
	k.mu.Unlock()
	return connect.NewResponse(&nodev1.BootstrapResponse{Applied: uint32(n)}), nil
}

// bootstrap commits the node to initialising etcd: the marker comes first, so a failure after
// it is retried by the next boot and never initialises a second cluster.
func (s *Server) bootstrap(ctx context.Context, k *Kubernetes, c k8s.Cluster) (chan struct{}, error) {
	bootstrapped, err := knode.Bootstrapped(k.Paths)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if bootstrapped {
		return nil, failed(connect.CodeFailedPrecondition, "the node is bootstrapped already")
	}
	if left, err := knode.Left(k.Paths); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	} else if left {
		return nil, failed(connect.CodeFailedPrecondition, "the node left etcd; reinstall it to join the cluster again")
	}
	hasData, err := knode.EtcdHasData(k.Paths)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if hasData {
		return nil, failed(connect.CodeFailedPrecondition, "%s holds etcd data; reset VAR before bootstrapping a new cluster on this node", k.Paths.EtcdData)
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	// The static pods advertise the node's address and read the certificates the preparation
	// writes, which runs at boot while chalkd answers already.
	switch problem, err := preparation(k.Paths); {
	case err != nil:
		return nil, failed(connect.CodeInternal, "%v", err)
	case problem == preparing:
		return nil, failed(connect.CodeFailedPrecondition, "%v; see chalkctl logs <node> --unit chalkos-kubernetes", errNotPrepared)
	case problem != "":
		return nil, failed(connect.CodeFailedPrecondition, "%s", problem)
	}
	ips, err := knode.ReadNodeIPs(k.Paths)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if !k.membership.TryLock() {
		return nil, failed(connect.CodeFailedPrecondition, "the node is joining the cluster at %s; chalkctl status shows its progress", c.Endpoint)
	}
	defer k.membership.Unlock()
	switch bootstrapped, err := knode.Bootstrapped(k.Paths); {
	case err != nil:
		return nil, failed(connect.CodeInternal, "%v", err)
	case bootstrapped:
		return nil, failed(connect.CodeFailedPrecondition, "the node joined the cluster already")
	}
	// The node may be an etcd member of the cluster already, even when it never learned so.
	switch joining, err := knode.Joining(k.Paths); {
	case err != nil:
		return nil, failed(connect.CodeInternal, "%v", err)
	case joining:
		return nil, failed(connect.CodeFailedPrecondition, "the node started joining the cluster at %s; finish the join or remove its member and reinstall", c.Endpoint)
	}
	if started, err := knode.HasInitialCluster(k.Paths); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	} else if started {
		return nil, failed(connect.CodeFailedPrecondition, "the node is joining the cluster at %s; chalkctl status shows its progress", c.Endpoint)
	}
	// A second bootstrap would start a second cluster.
	if k.ClusterAnswers(ctx, c, share) {
		return nil, failed(connect.CodeFailedPrecondition, "the cluster's API server answers at %s; this node joins it on its own", c.Endpoint)
	}
	// etcd's peers and the certificates know the node by its addresses from now on.
	if err := knode.WritePin(k.Paths, ips); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if err := knode.MarkBootstrapped(k.Paths, time.Now()); err != nil {
		return nil, failed(connect.CodeInternal, "record the bootstrap: %v", err)
	}
	if err := knode.RenderStaticPods(k.Paths); err != nil {
		return nil, failed(connect.CodeInternal, "render the static pods: %v", err)
	}
	log.Print("bootstrapped: the control plane starts")
	return k.start(), nil
}

// preparing is the state of a node whose Kubernetes files are being prepared.
const preparing = "preparing"

// preparation says why the node's Kubernetes files cannot be used: why the last preparation
// failed, preparing while one runs, or "" once one succeeded.
func preparation(p knode.Paths) (string, error) {
	switch reason, err := knode.PreparationError(p); {
	case err != nil:
		return "", err
	case reason != "":
		return "preparation failed: " + reason, nil
	}
	switch prepared, err := knode.Prepared(p); {
	case err != nil:
		return "", err
	case !prepared:
		return preparing, nil
	}
	return "", nil
}

// status describes the node's Kubernetes state.
func (k *Kubernetes) status(ctx context.Context) (*nodev1.KubernetesStatus, error) {
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return nil, err
	}
	st := &nodev1.KubernetesStatus{Kind: c.Kind}
	if _, err := os.Stat(k.Paths.Share()); errors.Is(err, os.ErrNotExist) {
		st.State = "no share"
		return st, nil
	}
	if problem, err := preparation(k.Paths); err != nil {
		return nil, err
	} else if problem != "" {
		st.State = problem
		return st, nil
	}
	left, err := knode.Left(k.Paths)
	if err != nil {
		return nil, err
	}
	switch bootstrapped, err := knode.Bootstrapped(k.Paths); {
	case err != nil:
		return nil, err
	case c.Kind == k8s.KindWorker:
		st.State = "joined"
	case left:
		st.State = "left etcd; reinstall the node to join the cluster again"
		return st, nil
	case bootstrapped:
		switch err := knode.CheckEtcdData(k.Paths); {
		case errors.Is(err, knode.ErrEtcdDataMissing):
			st.State = "etcd data missing: restore etcd or reinstall the node"
			return st, nil
		case err != nil:
			return nil, err
		}
		st.State = "bootstrapped"
		k.mu.Lock()
		for _, problem := range []string{k.pinProblem, k.split} {
			if problem != "" {
				st.State += ": " + problem
			}
		}
		k.mu.Unlock()
		if len(c.VIP.Addresses) > 0 {
			st.Vip = "standby"
			if k.vipHolder.Load() {
				st.Vip = "holder"
			}
		}
	default:
		if st.State, err = k.joinStatus(c); err != nil {
			return nil, err
		}
		return st, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ready, err := k.NodeReady(ctx)
	if err != nil {
		ready = fmt.Sprintf("unknown: %v", err)
	}
	st.NodeReady = ready
	return st, nil
}

// etcdRequest bounds one request to etcd.
func (k *Kubernetes) etcdRequest(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, k.etcdTimeout())
}

func (k *Kubernetes) etcdTimeout() time.Duration {
	if k.EtcdTimeout <= 0 {
		return 30 * time.Second
	}
	return k.EtcdTimeout
}

func (k *Kubernetes) localEtcd() string {
	if k.LocalEtcd != "" {
		return k.LocalEtcd
	}
	return localEtcd
}
