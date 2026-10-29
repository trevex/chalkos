package chalkd

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/pki"
)

// kubernetesLayer is chalkd's Kubernetes layer: it serves KubernetesService for the node's
// Kubernetes side.
type kubernetesLayer struct {
	node layer.Node
	k    *Kubernetes
}

// NewKubernetesLayer is the Kubernetes layer of the node, with its Kubernetes side.
func NewKubernetesLayer(node layer.Node, k *Kubernetes) layer.Plugin {
	return &kubernetesLayer{node: node, k: k}
}

func (p *kubernetesLayer) Name() string { return layers.Kubernetes }

func (p *kubernetesLayer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return kubernetesv1connect.NewKubernetesServiceHandler(p, opts...)
}

func (p *kubernetesLayer) Permissions() map[string]layer.Permission {
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
func (p *kubernetesLayer) Start(ctx context.Context) error {
	p.k.Start()
	<-ctx.Done()
	p.k.Stop()
	return nil
}

func (p *kubernetesLayer) Status(ctx context.Context, _ *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error) {
	st, err := p.k.status(ctx)
	if err != nil {
		return nil, failed(connect.CodeInternal, "kubernetes: %v", err)
	}
	return connect.NewResponse(st), nil
}
