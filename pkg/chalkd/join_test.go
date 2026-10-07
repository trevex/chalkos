package chalkd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	clientv3 "go.etcd.io/etcd/client/v3"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// joinPeerURL is where the joining node's etcd listens for its peers: the node's address in
// joiningServer, on etcd's peer port.
const joinPeerURL = "https://127.0.0.2:2380"

// joiningServer is a control-plane node at 127.0.0.2 that is not bootstrapped, and the running
// member cp0 of the cluster it finds at its endpoint.
func joiningServer(t *testing.T) (*Server, *etcdtest.Member, *clientv3.Client) {
	t.Helper()
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	if err := knode.Prepare(k.Paths, time.Now(), nodeIP("127.0.0.2"), nil); err != nil {
		t.Fatal(err)
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		t.Fatal(err)
	}
	cp0 := etcdtest.StartNew(t, *share.EtcdCA, "cp0")
	k.EtcdEndpoints = func(context.Context, k8s.Cluster, kpki.Share, []net.IP) ([]string, error) {
		return []string{cp0.ClientURL}, nil
	}
	k.ClusterAnswers = func(context.Context, k8s.Cluster, kpki.Share) bool { return true }
	k.JoinRetry = 50 * time.Millisecond
	cli, err := etcd.Dial([]string{cp0.ClientURL}, etcdtest.ClientTLS(t, *share.EtcdCA))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return s, cp0, cli
}

// eventually waits until check holds.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func kubernetesState(t *testing.T, s *Server) string {
	t.Helper()
	st, err := s.Kubernetes.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st.State
}

func members(t *testing.T, cli *clientv3.Client) []etcd.Member {
	t.Helper()
	list, err := etcd.Members(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A control plane that is not bootstrapped joins the cluster at its endpoint as a learner,
// starts etcd alone, and once promoted the rest of its control plane and its control plane's
// loop.
func TestJoin(t *testing.T) {
	s, _, cli := joiningServer(t)
	k := s.Kubernetes
	p := k.Paths
	share, _ := knode.ReadShare(p)
	applied := make(chan struct{})
	k.ControlPlane = func(ctx context.Context, _ kpki.Share, done func(int)) error {
		done(19)
		close(applied)
		<-ctx.Done()
		return ctx.Err()
	}
	k.Start()
	eventually(t, "etcd's static pod", func() bool { return exists(filepath.Join(p.Manifests(), "etcd.json")) })
	eventually(t, "the learner's state", func() bool { return strings.Contains(kubernetesState(t, s), "catches up") })
	if pods, _ := os.ReadDir(p.Manifests()); len(pods) != 1 {
		t.Errorf("static pods %v, want etcd alone while it is a learner", pods)
	}
	pin, err := knode.ReadPin(p)
	if err != nil || len(pin) != 1 || pin[0].String() != "127.0.0.2" {
		t.Fatalf("pin = %v, %v", pin, err)
	}
	// The peers know the learner, which has not started and so sorts first, by the pinned address.
	if list := members(t, cli); len(list) != 2 || !slices.Equal(list[0].PeerURLs, []string{etcd.PeerURL(net.IP(pin[0].AsSlice()))}) {
		t.Errorf("members %+v, want the learner at the pinned address %s", list, pin[0])
	}
	initial, err := os.ReadFile(p.EtcdInitialCluster())
	if err != nil {
		t.Fatal(err)
	}
	// What the kubelet does with etcd's static pod: etcd runs and writes its data.
	etcdtest.StartExisting(t, *share.EtcdCA, "n1", joinPeerURL, strings.TrimSpace(string(initial)))
	write(t, filepath.Join(p.EtcdData, "member", "snap", "db"), "")

	select {
	case <-applied:
	case <-time.After(time.Minute):
		t.Fatal("the control plane's loop did not start after the join")
	}
	if bootstrapped, _ := knode.Bootstrapped(p); !bootstrapped {
		t.Error("the joined node is not marked bootstrapped")
	}
	if initialised, _ := knode.EtcdInitialised(p); !initialised {
		t.Error("the joined node's etcd is not marked initialised")
	}
	if pods, _ := os.ReadDir(p.Manifests()); len(pods) != 4 {
		t.Errorf("static pods %v after the join", pods)
	}
	list := members(t, cli)
	if len(list) != 2 || list[1].Name != "n1" || list[1].Learner {
		t.Errorf("members %+v, want the voter n1", list)
	}
	if got := kubernetesState(t, s); got != "bootstrapped" {
		t.Errorf("state %q after the join", got)
	}
	// The node is a member now; it is never bootstrapped.
	_, err = bootstrap(s, context.Background())
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "bootstrapped already") {
		t.Errorf("bootstrap after the join: %v", err)
	}
}

// A member at the node's address that the node does not remember adding is stale: the join
// stops and names it until an operator removes it.
func TestJoinStopsAtStaleMember(t *testing.T) {
	s, _, cli := joiningServer(t)
	k := s.Kubernetes
	stale, _, err := etcd.AddLearner(context.Background(), cli, "n1", joinPeerURL)
	if err != nil {
		t.Fatal(err)
	}
	k.Start()
	want := "joining the cluster at https://192.168.100.11:6443: etcd has a member " + fmt.Sprintf("%x", stale.ID) + " (not started, " + joinPeerURL + ") already; remove it with chalkctl etcd remove-member " + fmt.Sprintf("%x", stale.ID)
	eventually(t, "the stale member's state", func() bool { return kubernetesState(t, s) == want })
	if exists(k.Paths.Pin()) || exists(k.Paths.EtcdInitialCluster()) {
		t.Error("a node that found a stale member pinned itself or prepared etcd")
	}
	if err := etcd.Remove(context.Background(), cli, stale.ID, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the join to go on", func() bool { return strings.Contains(kubernetesState(t, s), "catches up") })
	list := members(t, cli)
	if len(list) != 2 || list[1].ID == stale.ID {
		t.Errorf("members %+v, want cp0 and a new learner", list)
	}
}

// A node that pinned itself and added its learner continues with that learner.
func TestJoinResumes(t *testing.T) {
	s, _, cli := joiningServer(t)
	k := s.Kubernetes
	ips, err := knode.ReadNodeIPs(k.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := knode.MarkJoining(k.Paths, "https://192.168.100.11:6443"); err != nil {
		t.Fatal(err)
	}
	if err := knode.WritePin(k.Paths, ips); err != nil {
		t.Fatal(err)
	}
	added, _, err := etcd.AddLearner(context.Background(), cli, "n1", joinPeerURL)
	if err != nil {
		t.Fatal(err)
	}
	k.Start()
	eventually(t, "the learner's state", func() bool { return strings.Contains(kubernetesState(t, s), "catches up") })
	if list := members(t, cli); len(list) != 2 || !slices.ContainsFunc(list, func(m etcd.Member) bool { return m.ID == added.ID }) {
		t.Errorf("members %+v, want cp0 and the learner added before", list)
	}
	if !exists(k.Paths.EtcdInitialCluster()) {
		t.Error("the resumed join did not record its initial cluster")
	}
}

// Without a cluster at the endpoint the node waits, for a bootstrap or the cluster.
func TestJoinWaitsForCluster(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	k.JoinRetry = 10 * time.Millisecond
	var asked atomic.Int32
	k.EtcdEndpoints = func(context.Context, k8s.Cluster, kpki.Share, []net.IP) ([]string, error) {
		asked.Add(1)
		return nil, errors.New("connection refused")
	}
	k.Start()
	eventually(t, "the join to ask for the cluster", func() bool { return asked.Load() > 1 })
	if got := kubernetesState(t, s); got != "waiting for bootstrap or for the cluster at https://192.168.100.11:6443" {
		t.Errorf("state %q", got)
	}
	if exists(k.Paths.Pin()) {
		t.Error("a node waiting for the cluster pinned itself")
	}
	// A bootstrap ends the join.
	if _, err := bootstrap(s, context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the join to stop", func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return !k.joining
	})
}

func TestBootstrapRefusedWhileClusterAnswers(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	k.ClusterAnswers = func(context.Context, k8s.Cluster, kpki.Share) bool { return true }
	_, err := bootstrap(s, context.Background())
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the cluster's API server answers at https://192.168.100.11:6443") {
		t.Errorf("bootstrap: %v", err)
	}
	if exists(k.Paths.Pin()) || exists(k.Paths.Bootstrapped()) {
		t.Error("a refused bootstrap pinned or marked the node")
	}
}

func TestBootstrapRefusedWhileJoining(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	write(t, k.Paths.EtcdInitialCluster(), "cp0=https://192.168.100.10:2380,n1=https://192.168.100.11:2380\n")
	_, err := bootstrap(s, context.Background())
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "joining the cluster") {
		t.Errorf("bootstrap of a joining node: %v", err)
	}
	if err := os.Remove(k.Paths.EtcdInitialCluster()); err != nil {
		t.Fatal(err)
	}
	k.membership.Lock()
	_, err = bootstrap(s, context.Background())
	k.membership.Unlock()
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "joining the cluster") {
		t.Errorf("bootstrap during a join: %v", err)
	}
}

// stallJoin starts a join that cannot add its learner, as etcd takes one learner at a time and
// the cluster has another, and waits until it tried.
func stallJoin(t *testing.T, s *Server, cli *clientv3.Client) etcd.Member {
	t.Helper()
	k := s.Kubernetes
	other, _, err := etcd.AddLearner(context.Background(), cli, "other", etcdtest.PeerURL(t))
	if err != nil {
		t.Fatal(err)
	}
	k.Start()
	eventually(t, "the join to fail adding its learner", func() bool {
		return strings.Contains(kubernetesState(t, s), "add this node to etcd as a learner")
	})
	return other
}

func stopJoin(t *testing.T, k *Kubernetes) {
	t.Helper()
	k.Stop()
	eventually(t, "the join to stop", func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return !k.joining
	})
}

// A node whose learner may have been added, as when the reply to the addition was lost, never
// bootstraps a cluster of its own.
func TestBootstrapRefusedAfterJoinStarted(t *testing.T) {
	s, _, cli := joiningServer(t)
	k := s.Kubernetes
	p := k.Paths
	other := stallJoin(t, s, cli)
	stopJoin(t, k)
	// The member was added, but the node never learned of it.
	if err := etcd.Remove(context.Background(), cli, other.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := etcd.AddLearner(context.Background(), cli, "", joinPeerURL); err != nil {
		t.Fatal(err)
	}
	k.ClusterAnswers = func(context.Context, k8s.Cluster, kpki.Share) bool { return false }
	_, err := bootstrap(s, context.Background())
	want := "the node started joining the cluster at https://192.168.100.11:6443; finish the join or remove its member and reinstall"
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
		t.Errorf("bootstrap: %v, want %q", err, want)
	}
	if exists(p.Bootstrapped()) || exists(p.EtcdInitialCluster()) || exists(p.EtcdInitialised()) {
		t.Error("a refused bootstrap marked the node")
	}
	if pods, _ := os.ReadDir(p.Manifests()); len(pods) != 0 {
		t.Errorf("static pods %v after a refused bootstrap", pods)
	}
}

// Once the join started, the status names it and why it stalls, never a wait for the cluster.
func TestJoinStatusAfterStall(t *testing.T) {
	s, _, cli := joiningServer(t)
	k := s.Kubernetes
	var unreachable atomic.Bool
	endpoints := k.EtcdEndpoints
	k.EtcdEndpoints = func(ctx context.Context, c k8s.Cluster, share kpki.Share, self []net.IP) ([]string, error) {
		if unreachable.Load() {
			return nil, errors.New("connection refused")
		}
		return endpoints(ctx, c, share, self)
	}
	stallJoin(t, s, cli)
	const prefix = "joining the cluster at https://192.168.100.11:6443: "
	if got := kubernetesState(t, s); !strings.HasPrefix(got, prefix+"add this node to etcd as a learner") || !strings.HasSuffix(got, "; trying again") {
		t.Errorf("state %q", got)
	}
	unreachable.Store(true)
	eventually(t, "the unreachable cluster's state", func() bool { return kubernetesState(t, s) == prefix+"connection refused" })
	// chalkd started again remembers the join.
	fresh := &Kubernetes{Paths: k.Paths}
	st, err := fresh.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.State, prefix) {
		t.Errorf("state %q after a restart", st.State)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Requests to etcd end after the timeout, so a cluster that does not answer never holds up the
// join, which tries again.
func TestJoinBoundsEtcdRequests(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	k.JoinRetry = 10 * time.Millisecond
	k.EtcdTimeout = 100 * time.Millisecond
	silent := etcdtest.Silent(t)
	var asked atomic.Int32
	k.EtcdEndpoints = func(context.Context, k8s.Cluster, kpki.Share, []net.IP) ([]string, error) {
		asked.Add(1)
		return []string{silent}, nil
	}
	k.Start()
	eventually(t, "the join to try again", func() bool { return asked.Load() > 2 })
	eventually(t, "the unanswered request's state", func() bool {
		return strings.Contains(kubernetesState(t, s), "no etcd endpoint answered")
	})
	if exists(k.Paths.Joining()) || exists(k.Paths.Pin()) {
		t.Error("a join that reached no etcd member marked or pinned the node")
	}
	if !k.membership.TryLock() {
		t.Fatal("the join holds the membership lock")
	}
	k.membership.Unlock()
}

// Endpoints of two etcd clusters, as after two separate bootstraps, are refused before the node
// adds itself.
func TestJoinRefusesSplitClusters(t *testing.T) {
	s, cp0, cli := joiningServer(t)
	k := s.Kubernetes
	share, _ := knode.ReadShare(k.Paths)
	other := etcdtest.StartNew(t, *share.EtcdCA, "other")
	k.EtcdEndpoints = func(context.Context, k8s.Cluster, kpki.Share, []net.IP) ([]string, error) {
		return []string{cp0.ClientURL, other.ClientURL}, nil
	}
	k.Start()
	eventually(t, "the split's state", func() bool {
		return strings.Contains(kubernetesState(t, s), "the endpoints belong to different etcd clusters (")
	})
	if got := kubernetesState(t, s); !strings.Contains(got, "two nodes were bootstrapped separately") {
		t.Errorf("state %q", got)
	}
	stopJoin(t, k)
	if list := members(t, cli); len(list) != 1 {
		t.Errorf("members %+v, want cp0 alone", list)
	}
	otherCli, err := etcd.Dial([]string{other.ClientURL}, etcdtest.ClientTLS(t, *share.EtcdCA))
	if err != nil {
		t.Fatal(err)
	}
	defer otherCli.Close()
	if list := members(t, otherCli); len(list) != 1 {
		t.Errorf("members %+v of the other cluster, want it alone", list)
	}
	if exists(k.Paths.Joining()) || exists(k.Paths.Pin()) {
		t.Error("a refused join marked or pinned the node")
	}
}

// A bootstrapped control plane whose etcd belongs to another cluster than the other control
// planes' reports the split and changes nothing.
func TestControlPlaneReportsSplit(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	write(t, k.Paths.Bootstrapped(), "")
	share, _ := knode.ReadShare(k.Paths)
	local := etcdtest.StartNew(t, *share.EtcdCA, "n1")
	other := etcdtest.StartNew(t, *share.EtcdCA, "cp0")
	k.LocalEtcd = local.ClientURL
	k.EtcdTimeout = time.Second
	k.EtcdEndpoints = func(context.Context, k8s.Cluster, kpki.Share, []net.IP) ([]string, error) {
		return []string{other.ClientURL}, nil
	}
	k.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		<-ctx.Done()
		return ctx.Err()
	}
	k.Start()
	eventually(t, "the split's state", func() bool {
		return strings.HasPrefix(kubernetesState(t, s), "bootstrapped: the endpoints belong to different etcd clusters (")
	})
	for _, m := range []*etcdtest.Member{local, other} {
		cli, err := etcd.Dial([]string{m.ClientURL}, etcdtest.ClientTLS(t, *share.EtcdCA))
		if err != nil {
			t.Fatal(err)
		}
		if list := members(t, cli); len(list) != 1 {
			t.Errorf("members %+v, want %s alone", list, m.Name)
		}
		cli.Close()
	}
}

// A node that finds no member of its own and holds etcd data from an earlier membership refuses
// to join, and deletes nothing.
func TestJoinRefusesEarlierEtcdData(t *testing.T) {
	s, _, cli := joiningServer(t)
	k := s.Kubernetes
	db := filepath.Join(k.Paths.EtcdData, "member", "snap", "db")
	write(t, db, "")
	k.Start()
	want := "joining the cluster at https://192.168.100.11:6443: etcd data from an earlier membership is present on VAR; remove it with chalkctl etcd leave or reset VAR"
	eventually(t, "the refusal's state", func() bool { return kubernetesState(t, s) == want })
	stopJoin(t, k)
	if list := members(t, cli); len(list) != 1 {
		t.Errorf("members %+v, want cp0 alone", list)
	}
	if exists(k.Paths.Joining()) || exists(k.Paths.Pin()) {
		t.Error("a refused join marked or pinned the node")
	}
	if !exists(db) {
		t.Error("the join deleted etcd's data")
	}
}

// A learner removed while the node joins stops the join, which keeps what it wrote.
func TestJoinStopsWhenLearnerRemoved(t *testing.T) {
	s, _, cli := joiningServer(t)
	k := s.Kubernetes
	k.Start()
	eventually(t, "the learner's state", func() bool { return strings.Contains(kubernetesState(t, s), "catches up") })
	learner := slices.IndexFunc(members(t, cli), func(m etcd.Member) bool { return m.Learner })
	if learner < 0 {
		t.Fatal("no learner")
	}
	if err := etcd.Remove(context.Background(), cli, members(t, cli)[learner].ID, false); err != nil {
		t.Fatal(err)
	}
	want := "joining the cluster at https://192.168.100.11:6443: the node's etcd member was removed while joining; run chalkctl etcd leave and retry"
	eventually(t, "the join to stop", func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return !k.joining
	})
	if got := kubernetesState(t, s); got != want {
		t.Errorf("state %q, want %q", got, want)
	}
	if list := members(t, cli); len(list) != 1 {
		t.Errorf("members %+v, want cp0 alone", list)
	}
	for _, f := range []string{k.Paths.Joining(), k.Paths.Pin(), k.Paths.EtcdInitialCluster()} {
		if !exists(f) {
			t.Errorf("the stopped join removed %s", f)
		}
	}

	// A join resumed later stops again rather than adding another learner.
	k.setJoinStep("")
	k.Start()
	eventually(t, "the resumed join to stop", func() bool {
		k.mu.Lock()
		joining := k.joining
		k.mu.Unlock()
		return !joining && kubernetesState(t, s) == want
	})
	if list := members(t, cli); len(list) != 1 {
		t.Errorf("members %+v after resuming, want cp0 alone", list)
	}
}

// The join reports that its learner did not catch up in time, and tries again.
func TestJoinReportsPromoteTimeout(t *testing.T) {
	s, _, _ := joiningServer(t)
	k := s.Kubernetes
	k.PromoteTimeout = 300 * time.Millisecond
	k.Start()
	eventually(t, "the timeout's state", func() bool {
		return strings.Contains(kubernetesState(t, s), "did not catch up with the leader within 300ms; trying again")
	})
}
