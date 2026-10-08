package chalkd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	clientv3 "go.etcd.io/etcd/client/v3"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// addMember starts a member of the name and makes it a voter of the cluster cli reaches.
func addMember(t *testing.T, cli *clientv3.Client, ca pki.CertKey, name string) *etcdtest.Member {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	peerURL := etcdtest.PeerURL(t)
	var added etcd.Member
	var initial string
	var err error
	// etcd adds no member while one it just promoted is not yet in touch with the others.
	for {
		if added, initial, err = etcd.AddLearner(ctx, cli, name, peerURL); err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	m := etcdtest.StartExisting(t, ca, name, peerURL, initial)
	if err := etcd.Promote(ctx, cli, added.ID, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return m
}

// memberServer is the bootstrapped control-plane node n1, whose etcd member runs with the
// members named in others.
func memberServer(t *testing.T, others ...string) (*Server, *clientv3.Client, map[string]*etcdtest.Member) {
	t.Helper()
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	p := k.Paths
	share, err := knode.ReadShare(p)
	if err != nil {
		t.Fatal(err)
	}
	n1 := etcdtest.StartNew(t, *share.EtcdCA, "n1")
	members := map[string]*etcdtest.Member{"n1": n1}
	cli, err := etcd.Dial([]string{n1.ClientURL}, etcdtest.ClientTLS(t, *share.EtcdCA))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	for _, name := range others {
		members[name] = addMember(t, cli, *share.EtcdCA, name)
	}
	k.LocalEtcd = n1.ClientURL
	k.EtcdStopped = func(context.Context) error { return nil }
	ips, err := knode.ReadNodeIPs(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := knode.WritePin(p, ips); err != nil {
		t.Fatal(err)
	}
	write(t, p.Bootstrapped(), "")
	write(t, p.EtcdInitialised(), "")
	write(t, filepath.Join(p.EtcdData, "member", "snap", "db"), "")
	if err := knode.RenderStaticPods(p); err != nil {
		t.Fatal(err)
	}
	return s, cli, members
}

func etcdMembers(t *testing.T, s *Server) []*nodev1.EtcdMember {
	t.Helper()
	resp, err := s.EtcdMembers(context.Background(), connect.NewRequest(&nodev1.EtcdMembersRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.Members
}

func removeMember(s *Server, member string, force bool) error {
	_, err := s.EtcdRemoveMember(context.Background(), connect.NewRequest(&nodev1.EtcdRemoveMemberRequest{Member: member, Force: force}))
	return err
}

func leave(s *Server, force bool) error {
	_, err := s.EtcdLeave(context.Background(), connect.NewRequest(&nodev1.EtcdLeaveRequest{Force: force}))
	return err
}

// checkLeft checks that the node keeps nothing of its membership and stays out of etcd.
func checkLeft(t *testing.T, s *Server) {
	t.Helper()
	k := s.Kubernetes
	p := k.Paths
	if pods, err := os.ReadDir(p.Manifests()); err != nil || len(pods) != 0 {
		t.Errorf("static pods %v, %v after leaving", pods, err)
	}
	for _, f := range []string{p.Pin(), p.Bootstrapped(), p.EtcdInitialised(), p.EtcdInitialCluster(), p.Joining()} {
		if exists(f) {
			t.Errorf("%s outlived leaving", f)
		}
	}
	if hasData, _ := knode.EtcdHasData(p); hasData {
		t.Error("etcd's data outlived leaving")
	}
	if left, err := knode.Left(p); err != nil || !left {
		t.Errorf("Left() = %v, %v after leaving", left, err)
	}
	if got := kubernetesState(t, s); got != "left etcd; reinstall the node to join the cluster again" {
		t.Errorf("state %q", got)
	}
	// It neither joins nor bootstraps again.
	k.Start()
	k.mu.Lock()
	joining := k.joining
	k.mu.Unlock()
	if joining {
		t.Error("a node that left joins again")
	}
	if _, err := bootstrap(s, context.Background()); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "left etcd") {
		t.Errorf("bootstrap after leaving: %v", err)
	}
	if err := leave(s, false); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "left etcd") {
		t.Errorf("leaving again: %v", err)
	}
}

func TestEtcdMembers(t *testing.T) {
	s, _, _ := memberServer(t, "cp2")
	list := etcdMembers(t, s)
	if len(list) != 2 || list[0].Name != "cp2" || list[1].Name != "n1" {
		t.Fatalf("members %v", list)
	}
	for _, m := range list {
		if m.Learner || m.Unhealthy != "" || len(m.PeerUrls) != 1 || m.Id == 0 {
			t.Errorf("member %v, want a healthy voter", m)
		}
	}
}

func TestEtcdRemoveMember(t *testing.T) {
	s, cli, members := memberServer(t, "cp2", "cp3")
	if err := removeMember(s, "n1", false); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "chalkctl etcd leave n1") {
		t.Errorf("removing the node's own member: %v", err)
	}
	if err := removeMember(s, "cp9", false); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("removing an unknown member: %v", err)
	}
	members["cp3"].Stop()
	// Without cp2, n1 and the stopped cp3 would be left.
	if err := removeMember(s, "cp2", false); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "pass --force") {
		t.Errorf("removing cp2 while cp3 is down: %v", err)
	}
	list, err := etcd.Members(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	cp3, _ := etcd.Find(list, "cp3")
	// By its ID, as a member that never started has no name.
	if err := removeMember(s, fmt.Sprintf("%X", cp3.ID), false); err != nil {
		t.Fatalf("removing the stopped cp3: %v", err)
	}
	if list := etcdMembers(t, s); len(list) != 2 {
		t.Errorf("members %v, want cp2 and n1", list)
	}
}

func TestEtcdLeave(t *testing.T) {
	s, _, members := memberServer(t, "cp2")
	share, err := knode.ReadShare(s.Kubernetes.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := leave(s, false); err != nil {
		t.Fatal(err)
	}
	cp2, err := etcd.Dial([]string{members["cp2"].ClientURL}, etcdtest.ClientTLS(t, *share.EtcdCA))
	if err != nil {
		t.Fatal(err)
	}
	defer cp2.Close()
	if list, err := etcd.Members(context.Background(), cp2); err != nil || len(list) != 1 || list[0].Name != "cp2" {
		t.Errorf("members %v, %v after n1 left", list, err)
	}
	checkLeft(t, s)
}

// A node whose member an operator removed while it joined still leaves: etcd has nothing left to
// remove, and the node deletes what the join left behind.
func TestEtcdLeaveAfterMemberRemoved(t *testing.T) {
	s, _, cli := joiningServer(t)
	k := s.Kubernetes
	k.EtcdStopped = func(context.Context) error { return nil }
	// The node's own etcd does not answer; asking it ends after the timeout.
	k.EtcdTimeout = silentEtcdTimeout
	k.Start()
	eventually(t, "the learner's state", func() bool { return strings.Contains(kubernetesState(t, s), "catches up") })
	learner := slices.IndexFunc(members(t, cli), func(m etcd.Member) bool { return m.Learner })
	if learner < 0 {
		t.Fatal("no learner")
	}
	if err := etcd.Remove(context.Background(), cli, members(t, cli)[learner].ID, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the join to stop", func() bool {
		return strings.Contains(kubernetesState(t, s), "run chalkctl etcd leave and reinstall the node")
	})
	// The learner's etcd wrote its data.
	write(t, filepath.Join(k.Paths.EtcdData, "member", "snap", "db"), "")

	if err := leave(s, false); err != nil {
		t.Fatal(err)
	}
	if list := members(t, cli); len(list) != 1 || list[0].Name != "cp0" {
		t.Errorf("members %+v, want cp0 alone", list)
	}
	checkLeft(t, s)
}

// liveEtcdTimeout is how long a health check waits for each etcd member where all of them are up.
// The members run in the test's process, beside the other packages' tests, and on a busy machine
// one may take more than the three seconds of a node to answer the check's new connection.
const liveEtcdTimeout = 10 * time.Second

// silentEtcdTimeout bounds the requests to etcd where the node's own member does not answer.
// Every leave waits it out for that member, but it bounds the requests to the other members too,
// which on a busy machine take seconds to answer or to commit a member's removal.
const silentEtcdTimeout = 10 * time.Second

// otherMembersOnly makes the node's own etcd member stop answering, while the API server lists
// the other members.
func otherMembersOnly(t *testing.T, s *Server, others ...*etcdtest.Member) {
	t.Helper()
	k := s.Kubernetes
	k.LocalEtcd = etcdtest.Silent(t)
	k.EtcdTimeout = silentEtcdTimeout
	k.EtcdStatusTimeout = liveEtcdTimeout
	k.EtcdEndpoints = func(context.Context, k8s.Cluster, kpki.Share, []net.IP) ([]string, error) {
		var urls []string
		for _, m := range others {
			urls = append(urls, m.ClientURL)
		}
		return urls, nil
	}
}

// A bootstrapped node whose own etcd member does not answer leaves through the other members only
// when forced, as when it lost its pinned address and its etcd cannot run.
func TestEtcdLeaveNeedsLocalMemberOrForce(t *testing.T) {
	s, cli, others := memberServer(t, "cp2", "cp3")
	otherMembersOnly(t, s, others["cp2"], others["cp3"])
	err := leave(s, false)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not answer") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("leaving while the node's member does not answer: %v", err)
	}
	p := s.Kubernetes.Paths
	if !exists(p.Pin()) || !exists(p.Bootstrapped()) || exists(p.Left()) || len(members(t, cli)) != 3 {
		t.Error("a refused leave changed the node")
	}
	if pods, _ := os.ReadDir(p.Manifests()); len(pods) != 4 {
		t.Errorf("static pods %v after a refused leave", pods)
	}
	if err := leave(s, true); err != nil {
		t.Fatalf("forced leave: %v", err)
	}
	// n1's member stopped once removed.
	cli.SetEndpoints(others["cp2"].ClientURL)
	if list := members(t, cli); len(list) != 2 || slices.ContainsFunc(list, func(m etcd.Member) bool { return m.Name == "n1" }) {
		t.Errorf("members %+v after n1 left", list)
	}
	checkLeft(t, s)
}

// A bootstrapped node whose member an operator removed leaves without --force, although its etcd
// stopped answering.
func TestEtcdLeaveAfterOwnMemberRemoved(t *testing.T) {
	s, _, members := memberServer(t, "cp2", "cp3")
	share, err := knode.ReadShare(s.Kubernetes.Paths)
	if err != nil {
		t.Fatal(err)
	}
	cp2, err := etcd.Dial([]string{members["cp2"].ClientURL}, etcdtest.ClientTLS(t, *share.EtcdCA))
	if err != nil {
		t.Fatal(err)
	}
	defer cp2.Close()
	list, err := etcd.Members(context.Background(), cp2)
	if err != nil {
		t.Fatal(err)
	}
	n1, err := etcd.Find(list, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if err := etcd.Remove(context.Background(), cp2, n1.ID, false); err != nil {
		t.Fatal(err)
	}
	otherMembersOnly(t, s, members["cp2"], members["cp3"])
	if err := leave(s, false); err != nil {
		t.Fatal(err)
	}
	checkLeft(t, s)
}

// A node leaves only while the voters left keep a healthy quorum, and changes nothing otherwise.
func TestEtcdLeaveRefusedWithoutQuorum(t *testing.T) {
	s, _, members := memberServer(t, "cp2", "cp3")
	members["cp3"].Stop()
	err := leave(s, false)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "quorum") {
		t.Errorf("leaving while cp3 is down: %v", err)
	}
	p := s.Kubernetes.Paths
	if !exists(p.Pin()) || !exists(p.Bootstrapped()) || exists(p.Left()) || len(etcdMembers(t, s)) != 3 {
		t.Error("a refused leave changed the node")
	}
	if pods, _ := os.ReadDir(p.Manifests()); len(pods) != 4 {
		t.Errorf("static pods %v after a refused leave", pods)
	}
}

func TestEtcdNeedsMember(t *testing.T) {
	worker, _ := kubernetesServer(t, k8s.KindWorker, true)
	waiting, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	plain, _ := installedServer(t, section("", ""), false)
	for name, s := range map[string]*Server{"worker": worker, "not bootstrapped": waiting, "no Kubernetes": plain} {
		_, err := s.EtcdMembers(context.Background(), connect.NewRequest(&nodev1.EtcdMembersRequest{}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("members on %s: %v", name, err)
		}
		if err := leave(s, false); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("leaving on %s: %v", name, err)
		}
	}
}
