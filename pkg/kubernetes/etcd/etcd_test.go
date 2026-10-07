package etcd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	"github.com/trevex/chalkos/pkg/pki"
)

// joinAsLearner adds the node as a learner unless one of the members is its own, as a control
// plane joining does.
func joinAsLearner(ctx context.Context, cli *clientv3.Client, name, peerURL string, resume bool) (Member, string, error) {
	list, err := Members(ctx, cli)
	if err != nil {
		return Member{}, "", err
	}
	own, ok, err := Own(list, name, peerURL, resume)
	if err != nil || ok {
		return own, InitialCluster(list, own.ID, name), err
	}
	return AddLearner(ctx, cli, name, peerURL)
}

func dial(t *testing.T, ca pki.CertKey, members ...*etcdtest.Member) *clientv3.Client {
	t.Helper()
	var endpoints []string
	for _, m := range members {
		endpoints = append(endpoints, m.ClientURL)
	}
	cli, err := Dial(endpoints, etcdtest.ClientTLS(t, ca))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli
}

func members(t *testing.T, cli *clientv3.Client) []Member {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := Members(ctx, cli)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A node joins as a learner, starts its member with the initial cluster Join returns, and is
// promoted once it caught up.
func TestJoinAndPromote(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m1 := etcdtest.StartNew(t, ca, "m1")
	cli := dial(t, ca, m1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	peerURL := etcdtest.PeerURL(t)
	added, initial, err := joinAsLearner(ctx, cli, "m2", peerURL, false)
	if err != nil {
		t.Fatal(err)
	}
	if !added.Learner || added.Name != "" {
		t.Errorf("added %+v, want a learner that has not started", added)
	}
	if want := strings.Join(slices.Sorted(slices.Values([]string{"m1=" + m1.PeerURL, "m2=" + peerURL})), ","); initial != want {
		t.Errorf("initial cluster %q, want %q", initial, want)
	}
	if unhealthy := Health(ctx, cli, members(t, cli)); unhealthy[added.ID] == nil || len(unhealthy) != 1 {
		t.Errorf("unhealthy = %v, want the learner that has not started", unhealthy)
	}

	m2 := etcdtest.StartExisting(t, ca, "m2", peerURL, initial)
	if err := Promote(ctx, cli, added.ID, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	list := members(t, dial(t, ca, m2))
	if len(list) != 2 || list[0].Name != "m1" || list[1].Name != "m2" || list[0].Learner || list[1].Learner {
		t.Errorf("members %+v, want voters m1 and m2", list)
	}
	if unhealthy := Health(ctx, cli, list); len(unhealthy) != 0 {
		t.Errorf("unhealthy = %v", unhealthy)
	}
	// Promoting a voter changes nothing.
	if err := Promote(ctx, cli, added.ID, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Errorf("promote a voter: %v", err)
	}
}

// A node that remembers adding itself continues with its member; one that does not finds a
// stale member and stops.
func TestJoinResumesOrFindsStaleMember(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m1 := etcdtest.StartNew(t, ca, "m1")
	cli := dial(t, ca, m1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	peerURL := etcdtest.PeerURL(t)
	added, initial, err := joinAsLearner(ctx, cli, "m2", peerURL, false)
	if err != nil {
		t.Fatal(err)
	}
	// Not started yet: the member has no name, only the peer URL.
	again, againInitial, err := joinAsLearner(ctx, cli, "m2", peerURL, true)
	if err != nil || again.ID != added.ID || againInitial != initial {
		t.Errorf("resume before the start: %+v, %q, %v", again, againInitial, err)
	}
	if len(members(t, cli)) != 2 {
		t.Error("resuming added another member")
	}
	var stale *StaleMemberError
	if _, _, err := joinAsLearner(ctx, cli, "m2", peerURL, false); !errors.As(err, &stale) || stale.Member.ID != added.ID {
		t.Errorf("join without resume: %v, want the stale member", err)
	}

	etcdtest.StartExisting(t, ca, "m2", peerURL, initial)
	// Started: the member has the node's name.
	if again, _, err := joinAsLearner(ctx, cli, "m2", peerURL, true); err != nil || again.ID != added.ID {
		t.Errorf("resume after the start: %+v, %v", again, err)
	}
	if err := Promote(ctx, cli, added.ID, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	// A reinstalled node, which remembers nothing, finds its old member.
	_, _, err = joinAsLearner(ctx, cli, "m2", peerURL, false)
	if !errors.As(err, &stale) || stale.Member.Name != "m2" || err.Error() != "etcd has a member m2 already" {
		t.Errorf("reinstalled node: %v, want the stale member m2", err)
	}
	// A member of the name at another address is never the node's own.
	if _, _, err := joinAsLearner(ctx, cli, "m2", etcdtest.PeerURL(t), true); !errors.As(err, &stale) {
		t.Errorf("same name, other peer URL: %v, want the stale member", err)
	}
	// Nor is a member of another name at the node's address.
	if _, _, err := joinAsLearner(ctx, cli, "m3", peerURL, true); !errors.As(err, &stale) {
		t.Errorf("other name, same peer URL: %v, want the stale member", err)
	}
}

func TestOwn(t *testing.T) {
	const peer = "https://10.0.0.12:2380"
	started := Member{ID: 2, Name: "cp2", PeerURLs: []string{peer}}
	added := Member{ID: 2, PeerURLs: []string{peer}}
	other := Member{ID: 1, Name: "cp1", PeerURLs: []string{"https://10.0.0.11:2380"}}
	for name, tc := range map[string]struct {
		members []Member
		resume  bool
		own     bool
		stale   bool
	}{
		"new":                        {[]Member{other}, false, false, false},
		"resume without a member":    {[]Member{other}, true, false, false},
		"resume an added member":     {[]Member{other, added}, true, true, false},
		"resume a started member":    {[]Member{other, started}, true, true, false},
		"added member, no memory":    {[]Member{other, added}, false, false, true},
		"started member, no memory":  {[]Member{other, started}, false, false, true},
		"name at another peer URL":   {[]Member{other, {ID: 3, Name: "cp2", PeerURLs: []string{"https://10.0.0.22:2380"}}}, true, false, true},
		"another name at my address": {[]Member{other, {ID: 3, Name: "cp9", PeerURLs: []string{peer}}}, true, false, true},
	} {
		m, ok, err := Own(tc.members, "cp2", peer, tc.resume)
		var stale *StaleMemberError
		if ok != tc.own || errors.As(err, &stale) != tc.stale || (ok && m.ID != 2) {
			t.Errorf("%s: Own() = %+v, %v, %v", name, m, ok, err)
		}
	}
}

func TestCheckQuorum(t *testing.T) {
	voters := []Member{{ID: 1, Name: "cp1"}, {ID: 2, Name: "cp2"}, {ID: 3, Name: "cp3"}}
	withLearner := append(slices.Clone(voters), Member{ID: 4, Name: "cp4", Learner: true})
	down := func(ids ...uint64) map[uint64]error {
		m := map[uint64]error{}
		for _, id := range ids {
			m[id] = errors.New("down")
		}
		return m
	}
	for name, tc := range map[string]struct {
		members   []Member
		unhealthy map[uint64]error
		remove    uint64
		ok        bool
	}{
		"healthy voter of three":            {voters, nil, 3, true},
		"the down voter of three":           {voters, down(3), 3, true},
		"a healthy voter while one is down": {voters, down(3), 2, false},
		"a learner":                         {withLearner, down(1, 2, 3, 4), 4, true},
		"the last voter":                    {voters[:1], nil, 1, false},
		"one of two":                        {voters[:2], nil, 2, true},
		"unknown member":                    {voters, nil, 9, false},
	} {
		err := CheckQuorum(tc.members, tc.unhealthy, tc.remove)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	err := CheckQuorum(voters, down(3), 2)
	var quorum *QuorumError
	if !errors.As(err, &quorum) || err.Error() != "without cp2 etcd has 2 voters of which 1 are healthy, fewer than the 2 a quorum needs" {
		t.Errorf("err = %v", err)
	}
}

// join adds a voter of the name to the cluster cli reaches.
func join(t *testing.T, ctx context.Context, cli *clientv3.Client, ca pki.CertKey, name string) *etcdtest.Member {
	t.Helper()
	peerURL := etcdtest.PeerURL(t)
	var added Member
	var initial string
	var err error
	// etcd adds no member while one it just promoted is not yet in touch with the others.
	for {
		added, initial, err = joinAsLearner(ctx, cli, name, peerURL, false)
		if err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	m := etcdtest.StartExisting(t, ca, name, peerURL, initial)
	if err := Promote(ctx, cli, added.ID, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRemove(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m1 := etcdtest.StartNew(t, ca, "m1")
	cli := dial(t, ca, m1)
	// Three joins and four removals, each of which may wait for etcd to count on its members again.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	join(t, ctx, cli, ca, "m2")
	m3 := join(t, ctx, cli, ca, "m3")

	m3.Stop()
	list := members(t, cli)
	m2, _ := Find(list, "m2")
	down, _ := Find(list, "m3")
	// Without m2, m1 and the stopped m3 would be left: one healthy voter of two.
	var quorum *QuorumError
	if err := Remove(ctx, cli, m2.ID, false); !errors.As(err, &quorum) {
		t.Errorf("remove m2 while m3 is down: %v, want a quorum error", err)
	}
	if len(members(t, cli)) != 3 {
		t.Error("a refused removal removed a member")
	}
	// Removing the stopped member leaves two healthy voters.
	if err := Remove(ctx, cli, down.ID, false); err != nil {
		t.Fatalf("remove the stopped m3: %v", err)
	}
	if list := members(t, cli); len(list) != 2 || slices.ContainsFunc(list, func(m Member) bool { return m.Name == "m3" }) {
		t.Errorf("members %+v, want m1 and m2", list)
	}

	// A member that just joined goes too, once etcd counts on the others.
	m4 := join(t, ctx, cli, ca, "m4")
	list = members(t, cli)
	fresh, err := Find(list, m4.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, cli, fresh.ID, false); err != nil {
		t.Errorf("remove the member that just joined: %v", err)
	}

	// A learner that never started goes at any time.
	learner, _, err := joinAsLearner(ctx, cli, "m5", etcdtest.PeerURL(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, cli, learner.ID, false); err != nil {
		t.Errorf("remove the learner: %v", err)
	}
}

func TestFind(t *testing.T) {
	list := []Member{{ID: 0xabc, Name: "cp1"}, {ID: 0xdef}}
	for query, want := range map[string]uint64{"cp1": 0xabc, "abc": 0xabc, "DEF": 0xdef} {
		if m, err := Find(list, query); err != nil || m.ID != want {
			t.Errorf("Find(%q) = %+v, %v", query, m, err)
		}
	}
	if _, err := Find(list, "cp9"); err == nil {
		t.Error("found cp9")
	}
}

// Endpoints of one cluster agree; endpoints of two clusters, as after two separate bootstraps,
// are refused; endpoints that do not answer are left out.
func TestOneCluster(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m1 := etcdtest.StartNew(t, ca, "m1")
	cli := dial(t, ca, m1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m2 := join(t, ctx, cli, ca, "m2")
	other := etcdtest.StartNew(t, ca, "other")
	silent := etcdtest.Silent(t)

	// The members answer well within the deadline also on a busy machine. The silent endpoint
	// holds each call until its deadline, so the two calls run at once.
	var answered []string
	var err, splitErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
		defer scancel()
		answered, err = OneCluster(sctx, cli, []string{m1.ClientURL, m2.ClientURL, silent})
	})
	wg.Go(func() {
		sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
		defer scancel()
		_, splitErr = OneCluster(sctx, cli, []string{m1.ClientURL, other.ClientURL, silent})
	})
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{m1.ClientURL, m2.ClientURL}; !slices.Equal(answered, want) {
		t.Errorf("answered %v, want %v", answered, want)
	}

	err = splitErr
	var split *SplitError
	if !errors.As(err, &split) || !strings.HasPrefix(err.Error(), "the endpoints belong to different etcd clusters (") ||
		!strings.HasSuffix(err.Error(), "); two nodes were bootstrapped separately") ||
		!strings.Contains(err.Error(), m1.ClientURL) || !strings.Contains(err.Error(), other.ClientURL) {
		t.Errorf("err = %v, want the split", err)
	}

	start := time.Now()
	sctx, scancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer scancel()
	if _, err := OneCluster(sctx, cli, []string{silent}); err == nil || errors.As(err, &split) {
		t.Errorf("err = %v, want no endpoint answering", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("OneCluster took %v without an answer", d)
	}
}

// Promoting a learner that was removed fails at once.
func TestPromoteRemovedLearner(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m1 := etcdtest.StartNew(t, ca, "m1")
	cli := dial(t, ca, m1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	added, _, err := AddLearner(ctx, cli, "m2", etcdtest.PeerURL(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, cli, added.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := Promote(ctx, cli, added.ID, 100*time.Millisecond, 10*time.Second); !errors.Is(err, ErrMemberRemoved) {
		t.Errorf("err = %v, want ErrMemberRemoved", err)
	}
}

// Local finds the member that answers at an endpoint.
func TestLocal(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m1 := etcdtest.StartAdvertising(t, ca, "m1", "https://192.0.2.1:2380")
	cli := dial(t, ca, m1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	self, err := Local(ctx, cli, m1.ClientURL)
	if err != nil {
		t.Fatal(err)
	}
	if self.Name != "m1" || !slices.Equal(self.PeerURLs, []string{"https://192.0.2.1:2380"}) {
		t.Errorf("Local() = %+v", self)
	}
	sctx, scancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer scancel()
	if _, err := Local(sctx, cli, etcdtest.Silent(t)); err == nil {
		t.Error("found a member at an endpoint that does not answer")
	}
}
