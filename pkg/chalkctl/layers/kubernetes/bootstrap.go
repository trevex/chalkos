package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/strs"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

type bootstrapFlags struct {
	node    layer.NodeFlags
	timeout time.Duration
	// ignoreMembers bootstraps although another control plane is a member or is joining.
	ignoreMembers bool
}

func bootstrapCommand(h layer.Host) *cobra.Command {
	var f bootstrapFlags
	cmd := h.Command(&cobra.Command{
		Use:         "bootstrap <node>",
		Short:       "Initialise the cluster on a control plane",
		Long:        bootstrapLong,
		Example:     bootstrapExample,
		Args:        cli.ExactArgs(1),
		Annotations: layer.Meta{Credentials: layer.Admin}.Annotations(),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return bootstrap(h, ctx, f, pos) })
	f.node.RegisterClient(cmd.Flags())
	cmd.Flags().DurationVar(&f.timeout, "timeout", 20*time.Minute, "how long to wait for the control plane to apply its add-ons and Addon objects")
	cmd.Flags().BoolVar(&f.ignoreMembers, "ignore-members", false, "bootstrap although another control plane of the inventory is an etcd member or is joining, as when recovering a cluster whose other control planes are lost")
	return cmd
}

func bootstrap(h layer.Host, ctx context.Context, f bootstrapFlags, pos []string) error {
	t, err := h.Target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	// Without an inventory the node itself refuses when it is a worker.
	if !t.Cluster().Partial() {
		kind, err := kindOf(t.Cluster(), t.Name())
		if err != nil {
			return err
		}
		if kind != k8s.KindControlPlane {
			return fmt.Errorf("%s is not a control-plane node; bootstrap one of the cluster's control-plane nodes", t.Name())
		}
		if err := admitBootstrap(ctx, t.Cluster(), t.Name(), f.ignoreMembers, nodeStatusOf(t.Nodes().Call), h.Stderr()); err != nil {
			return err
		}
	}
	conn, err := t.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	fmt.Fprintf(h.Stdout(), "bootstrapping %s; the control plane pulls its images and starts\n", t.Name())
	resp, err := Client(conn).Bootstrap(ctx, &kubernetesv1.BootstrapRequest{})
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w", t.Name(), err)
	}
	fmt.Fprintf(h.Stdout(), "%s is bootstrapped; applied %s\n", t.Name(), strs.Count(resp.Applied, "object"))
	return nil
}

// admitBootstrap refuses to bootstrap the control plane called name while another control plane
// of the inventory is an etcd member or is joining, unless ignoreMembers. It warns on stderr of
// those that do not answer: chalkctl may reach only some nodes, as through a forwarded port.
func admitBootstrap(ctx context.Context, c layer.Cluster, name string, ignoreMembers bool, status statusFunc, stderr io.Writer) error {
	if ignoreMembers {
		return nil
	}
	others, err := nodesOf(c, k8s.KindControlPlane, name)
	if err != nil {
		return err
	}
	if err := noOtherMember(ctx, others, status, stderr); err != nil {
		return fmt.Errorf("%w; bootstrap only the first control plane of a new cluster, or pass --ignore-members to bootstrap %s anyway", err, name)
	}
	return nil
}

// noOtherMember refuses a bootstrap while one of the other control planes is an etcd member or is
// joining: a second bootstrap would start a second cluster behind the endpoint. One that does not
// answer gets a warning.
// A node whose image has no Kubernetes, and one that never joined or left etcd, is no member.
func noOtherMember(ctx context.Context, controlPlanes []string, status statusFunc, stderr io.Writer) error {
	var why []string
	for _, name := range controlPlanes {
		k, err := status(ctx, name)
		switch {
		case errors.Is(err, errNoKubernetes):
			continue
		case err != nil:
			fmt.Fprintf(stderr, "warning: %s does not say whether it is a member of a cluster: %v\n", name, err)
			continue
		}
		switch k.Phase {
		case kubernetesv1.Phase_PHASE_BOOTSTRAPPED, kubernetesv1.Phase_PHASE_JOINING, kubernetesv1.Phase_PHASE_ETCD_DATA_MISSING:
			why = append(why, name+": "+k.State)
		}
	}
	if len(why) == 0 {
		return nil
	}
	return fmt.Errorf("another control plane may hold the cluster (%s)", strings.Join(why, "; "))
}
