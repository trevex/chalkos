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

	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// errBootstrapped ends the join: the node was bootstrapped, or joined, meanwhile.
var errBootstrapped = errors.New("the node is bootstrapped")

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
	if k.joining {
		return
	}
	k.joining = true
	go k.superviseJoin(k.loops())
}

// setJoinState records what the join waits for or does, for the node's status.
func (k *Kubernetes) setJoinState(state string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.joinState = state
}

func (k *Kubernetes) joinStatus(c k8s.Cluster) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.joinState == "" {
		return waitingForCluster(c)
	}
	return k.joinState
}

// superviseJoin tries to join the cluster until the node is an etcd voter, then starts the
// control plane's loop. It stops when the node was bootstrapped instead.
func (k *Kubernetes) superviseJoin(ctx context.Context) {
	retry := k.JoinRetry
	if retry <= 0 {
		retry = 10 * time.Second
	}
	defer func() {
		k.mu.Lock()
		k.joining = false
		k.mu.Unlock()
	}()
	for {
		err := k.joinOnce(ctx)
		var waiting waitingError
		var stale *etcd.StaleMemberError
		switch {
		case err == nil:
			log.Print("kubernetes: joined the cluster; the control plane starts")
			k.setJoinState("")
			k.start()
			return
		case errors.Is(err, errBootstrapped):
			k.setJoinState("")
			return
		case errors.As(err, &waiting), errors.Is(err, errNotPrepared):
		case errors.As(err, &stale):
			log.Printf("kubernetes: %v", err)
		default:
			log.Printf("kubernetes: joining the cluster: %v; trying again in %v", err, retry)
			k.setJoinState(fmt.Sprintf("joining the cluster failed: %v; trying again", err))
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
		k.setJoinState(waitingForCluster(c))
		return waitingError{err}
	}
	k.setJoinState("joining the cluster at " + c.Endpoint)
	cred, err := newEtcdCredential(share, credentialValidity, time.Now)
	if err != nil {
		return err
	}
	tlsConfig, err := cred.tlsConfig()
	if err != nil {
		return err
	}
	cli, err := etcd.Dial(endpoints, tlsConfig)
	if err != nil {
		return err
	}
	defer cli.Close()

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
	members, err := etcd.Members(ctx, cli)
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
		k.setJoinState(fmt.Sprintf("%v; remove it with chalkctl etcd remove-member %s", err, name))
		return err
	}
	if err != nil {
		return err
	}
	var initialCluster string
	if ok {
		initialCluster = etcd.InitialCluster(members, own.ID, n.Name)
	} else {
		// The pin comes first: from here on the node may be a member, which its peers know by
		// these addresses.
		if pin == nil {
			if err := knode.WritePin(k.Paths); err != nil {
				return err
			}
		}
		if own, initialCluster, err = etcd.AddLearner(ctx, cli, n.Name, peerURL); err != nil {
			return err
		}
	}
	if err := knode.WriteInitialCluster(k.Paths, initialCluster); err != nil {
		return err
	}
	if err := knode.RenderEtcd(k.Paths); err != nil {
		return fmt.Errorf("render etcd: %w", err)
	}
	k.setJoinState(fmt.Sprintf("joining the cluster at %s: etcd member %x catches up", c.Endpoint, own.ID))
	pctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := etcd.Promote(pctx, cli, own.ID, 2*time.Second); err != nil {
		return err
	}
	if err := knode.MarkEtcdInitialised(k.Paths, time.Now()); err != nil {
		return err
	}
	if err := knode.RenderStaticPods(k.Paths); err != nil {
		return fmt.Errorf("render the static pods: %w", err)
	}
	// Last: a node marked bootstrapped renders its static pods at boot and joins no more.
	if err := install.WriteFile(k.Paths.Bootstrapped(), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return fmt.Errorf("record the join: %w", err)
	}
	return nil
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
