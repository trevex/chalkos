// Package kubernetes is chalkd's Kubernetes layer: it serves KubernetesService and runs the
// node's Kubernetes loops, the control plane's, the join and the VIP election.
package kubernetes

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

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
func (p *Plugin) Paths() knode.Paths { return p.k.paths }

// Stop ends the loops for good, as Start does once its context ends. Tests that ran the loops
// through deliveries stop them so.
func (p *Plugin) Stop() { p.k.stop() }

func (p *Plugin) Name() string { return layers.Kubernetes }

func (p *Plugin) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return kubernetesv1connect.NewKubernetesServiceHandler(p, opts...)
}

func (p *Plugin) Permissions() map[string]layer.Permission {
	return map[string]layer.Permission{
		kubernetesv1connect.KubernetesServiceStatusProcedure:               {Modes: layer.NormalOnly, Role: pki.RoleReader},
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

// Start runs the Kubernetes loops until ctx ends: on every way out the node releases the VIPs,
// which must not stay on a node whose chalkd no longer holds their lease, and closes its etcd
// clients.
func (p *Plugin) Start(ctx context.Context) error {
	p.k.startLoops()
	<-ctx.Done()
	p.k.stop()
	return nil
}

func (p *Plugin) Status(ctx context.Context, _ *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error) {
	st, err := p.k.status(ctx)
	if err != nil {
		return nil, failed(connect.CodeInternal, "kubernetes: %v", err)
	}
	return connect.NewResponse(st), nil
}

var _ layer.Actions = (*Plugin)(nil)

// Actions start chalkd's Kubernetes loops again: restart every loop, which reads the cluster when
// it starts; reload the control plane's loop, which reads the share and applies the control
// plane's objects.
func (p *Plugin) Actions() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"restart": func(context.Context) error { p.k.restart(); return nil },
		"reload":  func(context.Context) error { p.k.reloadControlPlane(); return nil },
	}
}

func failed(code connect.Code, format string, args ...any) error {
	return connect.NewError(code, fmt.Errorf(format, args...))
}

// count is n and the noun, in the plural unless n is one.
func count[N ~int | ~uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
