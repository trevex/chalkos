package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// errBootstrapped ends the join: the node was bootstrapped, or joined, meanwhile.
var errBootstrapped = errors.New("the node is bootstrapped")

// errMemberRemoved stops the join: an operator removed the member the node started etcd with,
// and joining again would start etcd with that member's data.
var errMemberRemoved = errors.New("the node's etcd member was removed while joining; run chalkctl etcd leave and reinstall the node")

// errEarlierData refuses a fresh join: etcd would start with the data of another membership.
var errEarlierData = errors.New("etcd data from an earlier membership is present on VAR; run chalkctl etcd leave and reinstall the node")

// waitingError means the cluster to join is not there yet.
type waitingError struct{ err error }

func (e waitingError) Error() string { return e.err.Error() }
func (e waitingError) Unwrap() error { return e.err }

// waitingForCluster is the state of a control-plane node that is neither bootstrapped nor
// finds a cluster to join at the endpoint.
func waitingForCluster(c k8s.Cluster) string {
	return "waiting for bootstrap or for the cluster at " + c.Endpoint
}

// startJoin runs the join's loop unless it runs already.
func (k *Kubernetes) startJoin() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.joining || k.stopped {
		return
	}
	k.joining = true
	k.joinDone = make(chan struct{})
	go k.superviseJoin(k.loops(), k.joinDone)
}

// setJoinStep records what the join does or why its last attempt failed, for the node's status.
func (k *Kubernetes) setJoinStep(step string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.joinStep, k.joinWaiting = step, false
}

// setJoinWaiting records that the join finds no cluster at the endpoint, and why.
func (k *Kubernetes) setJoinWaiting(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.joinStep, k.joinWaiting = err.Error(), true
}

// joinStatus is the state of a control plane that is not bootstrapped. Once the join started, as
// its marker on STATE says, it names the join and its step or last error, also while the
// cluster cannot be reached and after chalkd started again.
func (k *Kubernetes) joinStatus(c k8s.Cluster) (string, error) {
	joining, err := knode.Joining(k.Paths)
	if err != nil {
		return "", err
	}
	k.mu.Lock()
	step, waiting := k.joinStep, k.joinWaiting
	k.mu.Unlock()
	switch {
	case !joining && (waiting || step == ""):
		return waitingForCluster(c), nil
	case step == "":
		step = "resuming the join"
	}
	return "joining the cluster at " + c.Endpoint + ": " + step, nil
}

// superviseJoin tries to join the cluster until the node is an etcd voter, then starts the
// control plane's loop. It stops when the node was bootstrapped instead, and closes done once it
// ended.
func (k *Kubernetes) superviseJoin(ctx context.Context, done chan struct{}) {
	retry := k.JoinRetry
	if retry <= 0 {
		retry = 10 * time.Second
	}
	defer func() {
		k.mu.Lock()
		// Stop forgot this loop already, and Start may have run another.
		if k.joinDone == done {
			k.joining, k.joinDone = false, nil
		}
		k.mu.Unlock()
		close(done)
	}()
	// A join that keeps failing the same way logs it once.
	var logged string
	logChange := func(msg string) {
		if msg != logged {
			logged = msg
			log.Print(msg)
		}
	}
	for {
		err := k.joinOnce(ctx)
		var waiting waitingError
		var stale *etcd.StaleMemberError
		var split *etcd.SplitError
		switch {
		case err == nil:
			log.Print("kubernetes: joined the cluster; the control plane starts")
			k.setJoinStep("")
			k.mu.Lock()
			// Unless Stop ended the loops meanwhile.
			if k.ctx == ctx {
				k.startLocked()
			}
			k.mu.Unlock()
			return
		case errors.Is(err, errBootstrapped):
			k.setJoinStep("")
			return
		case errors.As(err, &waiting), errors.Is(err, errNotPrepared):
		case errors.As(err, &stale), errors.As(err, &split), errors.Is(err, errEarlierData):
			logChange(fmt.Sprintf("kubernetes: %v", err))
		case errors.Is(err, errMemberRemoved):
			log.Printf("kubernetes: %v", err)
			k.setJoinStep(err.Error())
			return
		default:
			logChange(fmt.Sprintf("kubernetes: joining the cluster: %v; trying again every %v", err, retry))
			k.setJoinStep(fmt.Sprintf("%v; trying again", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

// joinOnce makes the node an etcd voter of the cluster at the endpoint: it adds the node as a
// learner, starts its etcd and promotes it once it caught up, then renders the rest of the
// control plane. A node that remembers adding itself, by its pin, continues where it stopped; one
// that finds a member of its name otherwise stops, as that member is stale.
func (k *Kubernetes) joinOnce(ctx context.Context) error {
	if bootstrapped, err := knode.Bootstrapped(k.Paths); err != nil {
		return err
	} else if bootstrapped {
		return errBootstrapped
	}
	if prepared, err := knode.Prepared(k.Paths); err != nil {
		return err
	} else if !prepared {
		return errNotPrepared
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return err
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return err
	}
	n, err := k8s.ReadNode(k.Paths.NodeFile)
	if err != nil {
		return err
	}
	ips, err := knode.ReadNodeIPs(k.Paths)
	if err != nil {
		return err
	}
	endpoints, err := k.EtcdEndpoints(ctx, c, share, ips)
	if err != nil {
		k.setJoinWaiting(err)
		return waitingError{err}
	}
	k.setJoinStep("checking etcd's members")
	cli, err := k.dialEtcd(c, share, endpoints...)
	if err != nil {
		return err
	}
	defer cli.Close()
	// The node joins only members of one cluster, and talks only to those that said so.
	rctx, cancel := k.etcdRequest(ctx)
	answered, err := etcd.OneCluster(rctx, cli, endpoints)
	cancel()
	if split := (*etcd.SplitError)(nil); errors.As(err, &split) {
		k.setJoinStep(err.Error())
		return err
	}
	if err != nil {
		return err
	}
	cli.SetEndpoints(answered...)

	// Bootstrap refuses while the node joins.
	k.membership.Lock()
	defer k.membership.Unlock()
	if bootstrapped, err := knode.Bootstrapped(k.Paths); err != nil {
		return err
	} else if bootstrapped {
		return errBootstrapped
	}
	pin, err := knode.ReadPin(k.Paths)
	if err != nil {
		return err
	}
	peerURL := etcd.PeerURL(ips[0])
	rctx, cancel = k.etcdRequest(ctx)
	members, err := etcd.Members(rctx, cli)
	cancel()
	if err != nil {
		return err
	}
	own, ok, err := etcd.Own(members, n.Name, peerURL, pin != nil)
	var stale *etcd.StaleMemberError
	if errors.As(err, &stale) {
		name := stale.Member.Name
		if name == "" {
			name = fmt.Sprintf("%x", stale.Member.ID)
		}
		k.setJoinStep(fmt.Sprintf("%v; remove it with chalkctl etcd remove-member %s", err, name))
		return err
	}
	if err != nil {
		return err
	}
	if !ok {
		if err := k.checkFreshJoin(); err != nil {
			k.setJoinStep(err.Error())
			return err
		}
	}
	// The marker comes first: from here on the node may be a member, even when the reply to its
	// addition is lost, so it must never bootstrap a cluster of its own.
	if err := knode.MarkJoining(k.Paths, c.Endpoint); err != nil {
		return err
	}
	var initialCluster string
	if ok {
		initialCluster = etcd.InitialCluster(members, own.ID, n.Name)
	} else {
		// The peers know the member by the addresses it is added with.
		if pin == nil {
			if err := knode.WritePin(k.Paths, ips); err != nil {
				return err
			}
		}
		rctx, cancel := k.etcdRequest(ctx)
		own, initialCluster, err = etcd.AddLearner(rctx, cli, n.Name, peerURL)
		cancel()
		if err != nil {
			return err
		}
	}
	if err := knode.WriteInitialCluster(k.Paths, initialCluster); err != nil {
		return err
	}
	if err := knode.RenderEtcd(k.Paths); err != nil {
		return fmt.Errorf("render etcd: %w", err)
	}
	k.setJoinStep(fmt.Sprintf("etcd member %x catches up", own.ID))
	promoteTimeout := k.PromoteTimeout
	if promoteTimeout <= 0 {
		promoteTimeout = 10 * time.Minute
	}
	pctx, cancel := context.WithTimeout(ctx, promoteTimeout)
	defer cancel()
	switch err := etcd.Promote(pctx, cli, own.ID, 2*time.Second, k.etcdTimeout()); {
	case errors.Is(err, etcd.ErrMemberRemoved):
		return errMemberRemoved
	case err != nil && ctx.Err() == nil && pctx.Err() != nil:
		return fmt.Errorf("etcd member %x did not catch up with the leader within %v", own.ID, promoteTimeout)
	case err != nil:
		return err
	}
	if err := knode.MarkEtcdInitialised(k.Paths, time.Now()); err != nil {
		return err
	}
	if err := knode.RenderStaticPods(k.Paths); err != nil {
		return fmt.Errorf("render the static pods: %w", err)
	}
	// Last: a node marked bootstrapped renders its static pods at boot and joins no more.
	return knode.MarkJoined(k.Paths, time.Now())
}

// etcdEndpoints lists the client URLs of the etcd members other than the node's own, from the
// mirror pods of etcd's static pods the API server at the endpoint has.
func (k *Kubernetes) etcdEndpoints(ctx context.Context, c k8s.Cluster, share kpki.Share, self []net.IP) ([]string, error) {
	cfg, err := newCredential(share, credentialValidity, time.Now).restConfig(c.Endpoint)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 10 * time.Second
	defer cfg.Transport.(*http.Transport).CloseIdleConnections()
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	pods, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "component=etcd,tier=control-plane"})
	if err != nil {
		return nil, err
	}
	var endpoints []string
	for _, pod := range pods.Items {
		ip := net.ParseIP(pod.Status.PodIP)
		if ip == nil || slices.ContainsFunc(self, ip.Equal) {
			continue
		}
		endpoints = append(endpoints, etcd.ClientURL(ip))
	}
	if len(endpoints) == 0 {
		return nil, errors.New("the cluster lists no etcd members")
	}
	sort.Strings(endpoints)
	return endpoints, nil
}

// clusterAnswers reports whether the API server at the endpoint presents a certificate of the
// share's CA, as the cluster's API servers do.
func clusterAnswers(ctx context.Context, c k8s.Cluster, share kpki.Share) bool {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return false
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(share.CA.Certificate)) {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config:    &tls.Config{RootCAs: roots, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12},
	}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return false
	}
	conn.Close()
	return true
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
	others, err := k.EtcdEndpoints(ctx, c, share, ips)
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

// checkFreshJoin refuses adding the node to etcd when it finds no member of its own but etcd ran
// on it already: the member it started etcd with was removed, or its data is from an earlier
// membership. It deletes nothing.
func (k *Kubernetes) checkFreshJoin() error {
	if started, err := knode.HasInitialCluster(k.Paths); err != nil {
		return err
	} else if started {
		return errMemberRemoved
	}
	if hasData, err := knode.EtcdHasData(k.Paths); err != nil {
		return err
	} else if hasData {
		return errEarlierData
	}
	return nil
}
