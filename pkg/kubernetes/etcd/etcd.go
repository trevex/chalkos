// Package etcd manages the etcd membership of chalkos control-plane nodes: it lists the members
// and their health, adds a node as a learner and promotes it, and removes members only while
// the voters left keep a quorum.
package etcd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

// Member is an etcd member.
type Member struct {
	ID uint64
	// Name is the node's name; empty while a member that was added has not started.
	Name       string
	PeerURLs   []string
	ClientURLs []string
	Learner    bool
}

func (m Member) String() string {
	if m.Name == "" {
		return fmt.Sprintf("%x (not started, %s)", m.ID, strings.Join(m.PeerURLs, ","))
	}
	return m.Name
}

func member(m *etcdserverpb.Member) Member {
	return Member{ID: m.ID, Name: m.Name, PeerURLs: m.PeerURLs, ClientURLs: m.ClientURLs, Learner: m.IsLearner}
}

// PeerURL is the URL etcd's peers reach the member on the node with the address at.
func PeerURL(ip net.IP) string { return "https://" + net.JoinHostPort(ip.String(), "2380") }

// ClientURL is the URL clients reach the member on the node with the address at.
func ClientURL(ip net.IP) string { return "https://" + net.JoinHostPort(ip.String(), "2379") }

// Dial connects to etcd at the endpoints with the TLS configuration.
func Dial(endpoints []string, tlsConfig *tls.Config) (*clientv3.Client, error) {
	return clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		TLS:         tlsConfig,
		DialTimeout: 5 * time.Second,
		Logger:      zap.NewNop(),
	})
}

// Members lists the members as a quorum of the cluster agrees on them.
func Members(ctx context.Context, cli *clientv3.Client) ([]Member, error) {
	resp, err := cli.MemberList(ctx)
	if err != nil {
		return nil, fmt.Errorf("list etcd's members: %w", err)
	}
	members := make([]Member, len(resp.Members))
	for i, m := range resp.Members {
		members[i] = member(m)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return members, nil
}

// Health asks each member for its status at its client URLs and returns why each one that did
// not answer is unhealthy. A member that has not started has no client URLs.
func Health(ctx context.Context, cli *clientv3.Client, members []Member) map[uint64]error {
	var mu sync.Mutex
	unhealthy := map[uint64]error{}
	var wg sync.WaitGroup
	for _, m := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := status(ctx, cli, m)
			if err != nil {
				mu.Lock()
				unhealthy[m.ID] = err
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return unhealthy
}

func status(ctx context.Context, cli *clientv3.Client, m Member) error {
	if len(m.ClientURLs) == 0 {
		return errors.New("not started")
	}
	var err error
	for _, url := range m.ClientURLs {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = cli.Status(sctx, url)
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

// StaleMemberError means etcd has a member of the node's name, or one at its peer URL, that the
// node did not add in an earlier attempt it remembers: a node that was reinstalled without
// leaving etcd. It is never removed automatically.
type StaleMemberError struct {
	Member Member
}

func (e *StaleMemberError) Error() string {
	return fmt.Sprintf("etcd has a member %s already", e.Member)
}

// Own finds the member of the node of the name, reached at peerURL. With resume the node
// remembers adding itself before, so a member of its name, or one at its peer URL that has not
// started, is its own. Without resume, or when the name and peer URL do not belong together,
// such a member is stale. ok is false when no member is the node's.
func Own(members []Member, name, peerURL string, resume bool) (m Member, ok bool, err error) {
	for _, m := range members {
		atPeerURL := slices.Contains(m.PeerURLs, peerURL)
		if m.Name != name && !atPeerURL {
			continue
		}
		if !resume || (m.Name != name && m.Name != "") || !atPeerURL {
			return Member{}, false, &StaleMemberError{Member: m}
		}
		return m, true, nil
	}
	return Member{}, false, nil
}

// AddLearner adds the node of the name, reached at peerURL, as a learner. It returns the node's
// member and the initial cluster its etcd starts with.
func AddLearner(ctx context.Context, cli *clientv3.Client, name, peerURL string) (Member, string, error) {
	resp, err := cli.MemberAddAsLearner(ctx, []string{peerURL})
	if err != nil {
		return Member{}, "", fmt.Errorf("add this node to etcd as a learner: %w", err)
	}
	added := member(resp.Member)
	all := make([]Member, len(resp.Members))
	for i, m := range resp.Members {
		all[i] = member(m)
	}
	return added, InitialCluster(all, added.ID, name), nil
}

// InitialCluster is etcd's --initial-cluster for the member with the ID and name joining the
// members: every member's name and peer URLs.
func InitialCluster(members []Member, id uint64, name string) string {
	var entries []string
	for _, m := range members {
		n := m.Name
		if m.ID == id {
			n = name
		}
		for _, u := range m.PeerURLs {
			entries = append(entries, n+"="+u)
		}
	}
	sort.Strings(entries)
	return strings.Join(entries, ",")
}

// ErrMemberRemoved means the member is no longer one of etcd's.
var ErrMemberRemoved = errors.New("the member was removed")

// Promote makes the learner a voter once it caught up with the leader, trying again every
// interval while etcd says it has not. Each request ends after timeout. A member that is a voter
// already is left as it is.
func Promote(ctx context.Context, cli *clientv3.Client, id uint64, interval, timeout time.Duration) error {
	for {
		pctx, cancel := context.WithTimeout(ctx, timeout)
		_, err := cli.MemberPromote(pctx, id)
		cancel()
		switch {
		case err == nil, errors.Is(err, rpctypes.ErrMemberNotLearner):
			return nil
		case errors.Is(err, rpctypes.ErrMemberNotFound):
			return fmt.Errorf("promote the learner %x: %w", id, ErrMemberRemoved)
		case !errors.Is(err, rpctypes.ErrMemberLearnerNotReady):
			return fmt.Errorf("promote the learner %x: %w", id, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("promote the learner %x: it did not catch up with the leader: %w", id, ctx.Err())
		case <-time.After(interval):
		}
	}
}

// CheckQuorum refuses removing the member with the ID when the voters left would have fewer
// healthy members than their quorum. Removing a learner never changes the quorum.
func CheckQuorum(members []Member, unhealthy map[uint64]error, id uint64) error {
	i := slices.IndexFunc(members, func(m Member) bool { return m.ID == id })
	if i < 0 {
		return fmt.Errorf("etcd has no member %x", id)
	}
	if members[i].Learner {
		return nil
	}
	voters, healthy := 0, 0
	for _, m := range members {
		if m.Learner || m.ID == id {
			continue
		}
		voters++
		if unhealthy[m.ID] == nil {
			healthy++
		}
	}
	if voters == 0 {
		return fmt.Errorf("%s is etcd's last voter", members[i])
	}
	if quorum := voters/2 + 1; healthy < quorum {
		return &QuorumError{Member: members[i], Voters: voters, Healthy: healthy, Quorum: quorum}
	}
	return nil
}

// QuorumError means the voters left after removing a member would have fewer healthy members
// than their quorum.
type QuorumError struct {
	Member                  Member
	Voters, Healthy, Quorum int
}

func (e *QuorumError) Error() string {
	return fmt.Sprintf("without %s etcd has %d voters of which %d are healthy, fewer than the %d a quorum needs", e.Member, e.Voters, e.Healthy, e.Quorum)
}

// Remove removes the member with the ID, unless the voters left would lose their quorum and
// force is not set.
func Remove(ctx context.Context, cli *clientv3.Client, id uint64, force bool) error {
	members, err := Members(ctx, cli)
	if err != nil {
		return err
	}
	err = CheckQuorum(members, Health(ctx, cli, members), id)
	if quorum := (*QuorumError)(nil); err != nil && !(force && errors.As(err, &quorum)) {
		return err
	}
	// etcd refuses to change its members while one it counts on has been in touch only briefly,
	// as after it started or joined.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		_, err := cli.MemberRemove(ctx, id)
		if err == nil {
			return nil
		}
		if !errors.Is(err, rpctypes.ErrUnhealthy) {
			return fmt.Errorf("remove the member %x: %w", id, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("remove the member %x: %w", id, err)
		case <-time.After(time.Second):
		}
	}
}

// Find returns the member with the name or, given in hex, the ID.
func Find(members []Member, nameOrID string) (Member, error) {
	for _, m := range members {
		if m.Name == nameOrID || fmt.Sprintf("%x", m.ID) == strings.ToLower(nameOrID) {
			return m, nil
		}
	}
	return Member{}, fmt.Errorf("etcd has no member %s", nameOrID)
}

// SplitError means endpoints belong to different etcd clusters: two nodes were bootstrapped
// separately.
type SplitError struct {
	// IDs are the cluster IDs of the endpoints.
	IDs map[string]uint64
}

func (e *SplitError) Error() string {
	var ids []string
	for endpoint, id := range e.IDs {
		ids = append(ids, fmt.Sprintf("%x at %s", id, endpoint))
	}
	sort.Strings(ids)
	return fmt.Sprintf("the endpoints belong to different etcd clusters (%s); two nodes were bootstrapped separately", strings.Join(ids, ", "))
}

// OneCluster asks each endpoint which cluster it belongs to and returns the ones that answered,
// in the order given. It fails with a *SplitError when they belong to different clusters, and
// when none answered before ctx ended.
func OneCluster(ctx context.Context, cli *clientv3.Client, endpoints []string) ([]string, error) {
	ids := make([]uint64, len(endpoints))
	errs := make([]error, len(endpoints))
	var wg sync.WaitGroup
	for i, endpoint := range endpoints {
		wg.Go(func() {
			resp, err := cli.Status(ctx, endpoint)
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", endpoint, err)
				return
			}
			ids[i] = resp.Header.ClusterId
		})
	}
	wg.Wait()
	var answered []string
	byEndpoint := map[string]uint64{}
	for i, endpoint := range endpoints {
		if errs[i] == nil {
			answered = append(answered, endpoint)
			byEndpoint[endpoint] = ids[i]
		}
	}
	if len(answered) == 0 {
		return nil, fmt.Errorf("no etcd endpoint answered: %w", errors.Join(errs...))
	}
	for _, id := range byEndpoint {
		if id != byEndpoint[answered[0]] {
			return nil, &SplitError{IDs: byEndpoint}
		}
	}
	return answered, nil
}

// Local returns the member that answers at the endpoint, such as a node's own.
func Local(ctx context.Context, cli *clientv3.Client, endpoint string) (Member, error) {
	st, err := cli.Status(ctx, endpoint)
	if err != nil {
		return Member{}, fmt.Errorf("ask etcd at %s for its member: %w", endpoint, err)
	}
	members, err := Members(ctx, cli)
	if err != nil {
		return Member{}, err
	}
	for _, m := range members {
		if m.ID == st.Header.MemberId {
			return m, nil
		}
	}
	return Member{}, fmt.Errorf("etcd at %s answers as the member %x, which is not one of its members", endpoint, st.Header.MemberId)
}
