package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/manifest"
)

func (a *app) etcd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "members":
		return a.etcdMembers(ctx, args[1:])
	case "remove-member":
		return a.etcdRemoveMember(ctx, args[1:])
	case "leave":
		return a.etcdLeave(ctx, args[1:])
	}
	return errUsage
}

// throughControlPlane calls call on the control-plane node via, or else on the cluster's
// control-plane nodes but skip in name order until one answers.
func (a *app) throughControlPlane(ctx context.Context, n nodeCommand, via, skip string, call func(conn *client.Conn) error) error {
	c, err := a.loadCluster(ctx, n.cluster)
	if err != nil {
		return err
	}
	names := []string{via}
	if via == "" {
		if n.endpoint != "" {
			return errors.New("--endpoint reaches one node; name it with --via")
		}
		names = controlPlaneNodes(c.manifest, skip)
		if len(names) == 0 {
			return errors.New("the cluster has no other control-plane node to ask")
		}
	}
	secrets, err := a.loadSecrets(ctx, n.secrets, n.cluster.flake)
	if err != nil {
		return err
	}
	var last error
	for _, name := range names {
		node, err := c.node(name)
		if err != nil {
			return err
		}
		addr, err := endpoint(n.endpoint, name, node.Identity)
		if err != nil {
			return err
		}
		conn, err := dialInstalled(&target{cluster: c, name: name, node: node, secrets: secrets, addr: addr})
		if err != nil {
			return err
		}
		err = call(conn)
		if err == nil {
			return nil
		}
		// A node that is down cannot answer; another one may.
		if code := connect.CodeOf(err); via != "" || code != connect.CodeUnavailable && code != connect.CodeDeadlineExceeded {
			return fmt.Errorf("%s: %w", name, err)
		}
		last = fmt.Errorf("%s: %w", name, err)
	}
	return fmt.Errorf("no control-plane node answered: %w", last)
}

// controlPlaneNodes returns the cluster's control-plane nodes but skip, in name order.
func controlPlaneNodes(m *manifest.Manifest, skip string) []string {
	var names []string
	for name, node := range m.Nodes {
		if name != skip && m.Roles[node.Role].Kind == manifest.KindControlPlane {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (a *app) etcdMembers(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("etcd members", flag.ContinueOnError)
	var n nodeCommand
	n.register(fs)
	via := fs.String("via", "", "control-plane node to ask (default the first one that answers)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New("usage: chalkctl etcd members [--via NODE]")
	}
	var members []*nodev1.EtcdMember
	err = a.throughControlPlane(ctx, n, *via, "", func(conn *client.Conn) error {
		resp, err := conn.EtcdMembers(ctx, connect.NewRequest(&nodev1.EtcdMembersRequest{}))
		if err == nil {
			members = resp.Msg.Members
		}
		return err
	})
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tPEER URLS\tMEMBER\tHEALTH")
	for _, m := range members {
		name, kind, health := m.Name, "voter", "healthy"
		if name == "" {
			name = "(not started)"
		}
		if m.Learner {
			kind = "learner"
		}
		if m.Unhealthy != "" {
			health = "unhealthy: " + m.Unhealthy
		}
		fmt.Fprintf(w, "%s\t%x\t%s\t%s\t%s\n", name, m.Id, strings.Join(m.PeerUrls, ","), kind, health)
	}
	return w.Flush()
}

func (a *app) etcdRemoveMember(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("etcd remove-member", flag.ContinueOnError)
	var n nodeCommand
	n.register(fs)
	via := fs.String("via", "", "control-plane node that removes the member (default the first other one that answers)")
	force := fs.Bool("force", false, "remove the member even when the voters left would have no healthy quorum")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl etcd remove-member <node|id> [--via NODE] [--force]")
	}
	var removed *nodev1.EtcdMember
	err = a.throughControlPlane(ctx, n, *via, pos[0], func(conn *client.Conn) error {
		resp, err := conn.EtcdRemoveMember(ctx, connect.NewRequest(&nodev1.EtcdRemoveMemberRequest{Member: pos[0], Force: *force}))
		if err == nil {
			removed = resp.Msg.Removed
		}
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "removed the etcd member %s (%x)\n", pos[0], removed.Id)
	return nil
}

func (a *app) etcdLeave(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("etcd leave", flag.ContinueOnError)
	var n nodeCommand
	n.register(fs)
	force := fs.Bool("force", false, "leave through the other members also when the node's own etcd member does not answer, as when it lost its pinned address")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl etcd leave <node> [--force]")
	}
	t, err := a.target(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	if _, err := conn.EtcdLeave(ctx, connect.NewRequest(&nodev1.EtcdLeaveRequest{Force: *force})); err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	fmt.Fprintf(a.stdout, "%s left etcd; reinstall it to join the cluster again\n", t.name)
	return nil
}
