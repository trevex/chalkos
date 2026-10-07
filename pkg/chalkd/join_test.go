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
	if pin, err := knode.ReadPin(p); err != nil || len(pin) != 1 || pin[0].String() != "127.0.0.2" {
		t.Errorf("pin = %v, %v", pin, err)
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
	want := "etcd has a member " + fmt.Sprintf("%x", stale.ID) + " (not started, " + joinPeerURL + ") already; remove it with chalkctl etcd remove-member " + fmt.Sprintf("%x", stale.ID)
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
	if err := knode.WritePin(k.Paths); err != nil {
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

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
