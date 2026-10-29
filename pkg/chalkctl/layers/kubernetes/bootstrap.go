package kubernetes

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

type bootstrapFlags struct {
	node    layer.NodeFlags
	timeout time.Duration
}

func bootstrapCommand(h layer.Host) *cobra.Command {
	var f bootstrapFlags
	cmd := h.Command(&cobra.Command{
		Use:     "bootstrap <node>",
		Short:   "Initialise the cluster on a control plane",
		Long:    bootstrapLong,
		Example: bootstrapExample,
		Args:    cli.ExactArgs(1),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return bootstrap(h, ctx, f, pos) })
	f.node = h.NodeFlags(cmd.Flags())
	cmd.Flags().DurationVar(&f.timeout, "timeout", 20*time.Minute, "how long to wait for the control plane to apply its add-ons and Addon objects")
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
	}
	conn, err := t.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	fmt.Fprintf(h.Stdout(), "bootstrapping %s; the control plane pulls its images and starts\n", t.Name())
	resp, err := Client(conn).Bootstrap(ctx, connect.NewRequest(&kubernetesv1.BootstrapRequest{}))
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w", t.Name(), err)
	}
	fmt.Fprintf(h.Stdout(), "%s is bootstrapped; applied %s\n", t.Name(), layer.Count(resp.Msg.Applied, "object"))
	return nil
}
