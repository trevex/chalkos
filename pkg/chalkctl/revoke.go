package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
)

// revokeFlags are the flags of node revoke and node unrevoke.
type revokeFlags struct {
	node  nodeCommand
	via   string
	force bool
}

func (a *app) nodeRevokeCommand() *cobra.Command {
	var f revokeFlags
	cmd := a.command(&cobra.Command{
		Use:     "revoke <node>",
		Short:   "Cut a node off the cluster by name",
		Long:    nodeRevokeLong,
		Example: nodeRevokeExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeRevoke(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	cmd.Flags().StringVar(&f.via, "via", "", "control plane that revokes the node (default the first one that answers)")
	return cmd
}

func (a *app) nodeRevoke(ctx context.Context, f revokeFlags, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalkctl node revoke <node> [--via NODE]")
	}
	var resp *nodev1.RevokeNodeResponse
	err := a.throughControlPlane(ctx, f.node, f.via, "", func(conn *client.Conn) error {
		r, err := conn.RevokeNode(ctx, connect.NewRequest(&nodev1.RevokeNodeRequest{Node: pos[0]}))
		if err == nil {
			resp = r.Msg
		}
		return err
	})
	if err != nil {
		return err
	}
	deleted := "deleted its Node object"
	if !resp.NodeDeleted {
		deleted = "it had no Node object to delete"
	}
	fmt.Fprintf(a.stdout, "revoked node %s (since %s); %s\n", pos[0], resp.RevokedAt.AsTime().UTC().Format(time.RFC3339), deleted)
	return nil
}

func (a *app) nodeUnrevokeCommand() *cobra.Command {
	var f revokeFlags
	cmd := a.command(&cobra.Command{
		Use:     "unrevoke <node>",
		Short:   "Let a revoked node's name join the cluster again",
		Long:    nodeUnrevokeLong,
		Example: nodeUnrevokeExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeUnrevoke(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.registerClient(fs)
	fs.StringVar(&f.via, "via", "", "control plane that unrevokes the node (default the first one that answers)")
	fs.BoolVar(&f.force, "force", false, "unrevoke the node although the OS CA or the Kubernetes CA predates its revocation, so its old certificates work again")
	return cmd
}

func (a *app) nodeUnrevoke(ctx context.Context, f revokeFlags, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalkctl node unrevoke <node> [--via NODE] [--force]")
	}
	var resp *nodev1.UnrevokeNodeResponse
	err := a.throughControlPlane(ctx, f.node, f.via, "", func(conn *client.Conn) error {
		r, err := conn.UnrevokeNode(ctx, connect.NewRequest(&nodev1.UnrevokeNodeRequest{Node: pos[0], Force: f.force}))
		if err == nil {
			resp = r.Msg
		}
		return err
	})
	if err != nil {
		return err
	}
	var at time.Time
	if resp.RevokedAt != nil {
		at = resp.RevokedAt.AsTime()
	}
	fmt.Fprintln(a.stdout, unrevokedLine(pos[0], at, resp.StaleCas))
	return nil
}

// unrevokedLine says what unrevoking the node revoked at means, with the CAs that predate the
// revocation; at is zero when the time is unknown.
func unrevokedLine(name string, at time.Time, stale []string) string {
	when := "its revocation, at an unknown time,"
	if !at.IsZero() {
		when = at.UTC().Format(time.RFC3339)
	}
	if len(stale) == 0 {
		return fmt.Sprintf("unrevoked node %s; the certificates it held before %s are no longer trusted", name, when)
	}
	verb := "was"
	if len(stale) > 1 {
		verb = "were"
	}
	return fmt.Sprintf("unrevoked node %s; its certificates from before %s work again until they expire: the %s %s not rotated since",
		name, when, strings.Join(stale, " and "), verb)
}

// revokedLines are the status lines of the nodes a control plane lists as revoked, or of why
// it could not read them.
func revokedLines(k *nodev1.KubernetesStatus) []string {
	if k.RevokedNodesError != "" {
		return []string{"revoked nodes unknown: " + k.RevokedNodesError}
	}
	if len(k.RevokedNodes) == 0 {
		return nil
	}
	lines := []string{"revoked nodes:"}
	for _, n := range k.RevokedNodes {
		lines = append(lines, "  "+n.Name+"\t"+revokedSince(n))
	}
	return lines
}

// revokedSince says when a node was revoked.
func revokedSince(n *nodev1.RevokedNode) string {
	if n.RevokedAt == nil {
		return "at an unknown time"
	}
	return "since " + n.RevokedAt.AsTime().UTC().Format(time.RFC3339)
}
