// Package kubernetes is chalkd's Kubernetes layer: it serves KubernetesService and runs the
// node's Kubernetes loops, the control plane's, the join and the VIP election.
package kubernetes

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/pki"
)

// Plugin is chalkd's Kubernetes layer: it serves KubernetesService for the node's Kubernetes
// side.
type Plugin struct {
	node layer.Node
	k    *side
}

var _ layer.Plugin = (*Plugin)(nil)

// New is the Kubernetes layer of the node: its Kubernetes side with the paths, hooks and timing
// the options give, chalkd's own for zero fields.
func New(node layer.Node, o Options) *Plugin { return &Plugin{node: node, k: newSide(o)} }

// Paths are the files the node's Kubernetes side reads and writes; tests write them.
func (p *Plugin) Paths() knode.Paths { return p.k.state.Paths }

// Stop ends the loops for good, as Start does once its context ends. Tests that ran the loops
// through deliveries stop them so.
func (p *Plugin) Stop() { p.k.stop() }

func (p *Plugin) Name() string { return layers.Kubernetes }

func (p *Plugin) Register(server *connect.Server) {
	kubernetesv1connect.RegisterKubernetesServiceHandler(server, p)
}

func (p *Plugin) Permissions() map[string]layer.Permission {
	return map[string]layer.Permission{
		kubernetesv1connect.KubernetesServiceStatusProcedure:               {Modes: layer.NormalOnly, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
		kubernetesv1connect.KubernetesServiceBGPStatusProcedure:            {Modes: layer.NormalOnly, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
		kubernetesv1connect.KubernetesServiceBootstrapProcedure:            {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
		kubernetesv1connect.KubernetesServiceEtcdMembersProcedure:          {Modes: layer.NormalOnly, Role: pki.RoleReader},
		kubernetesv1connect.KubernetesServiceEtcdRemoveMemberProcedure:     {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
		kubernetesv1connect.KubernetesServiceEtcdLeaveProcedure:            {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
		kubernetesv1connect.KubernetesServiceRenewNodeCertificateProcedure: {Modes: layer.NormalOnly, Role: pki.RoleNode},
		kubernetesv1connect.KubernetesServiceRotationStepProcedure:         {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
		kubernetesv1connect.KubernetesServiceDrainNodeProcedure:            {Modes: layer.NormalOnly, Role: pki.RoleOperator},
		kubernetesv1connect.KubernetesServiceUncordonNodeProcedure:         {Modes: layer.NormalOnly, Role: pki.RoleOperator},
		kubernetesv1connect.KubernetesServiceRevokeNodeProcedure:           {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
		kubernetesv1connect.KubernetesServiceUnrevokeNodeProcedure:         {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
	}
}

// Start runs the loops until ctx ends, then stops them, which releases the VIPs and closes the
// etcd clients.
func (p *Plugin) Start(ctx context.Context) error {
	p.k.startLoops()
	<-ctx.Done()
	p.k.stop()
	return nil
}

func (p *Plugin) Status(ctx context.Context, _ *kubernetesv1.StatusRequest) (*kubernetesv1.StatusResponse, error) {
	st, err := p.k.status(ctx)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: %w", err)
	}
	return st, nil
}

var _ layer.Actions = (*Plugin)(nil)

// Actions start chalkd's Kubernetes loops again: restart every loop, which reads the cluster when
// it starts; reload the control plane's loop, which reads the share and applies the control
// plane's objects.
func (p *Plugin) Actions() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"restart": func(ctx context.Context) error { p.k.restart(ctx); return nil },
		"reload":  func(context.Context) error { p.k.reloadControlPlane(); return nil },
	}
}
