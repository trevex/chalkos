package chalkctl

import (
	"context"
	"errors"
	"slices"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/pki"
)

// caRotation runs the rotations every node takes part in, whatever its layers: chalkctl delivers
// them through ApplyConfig. r is nil where only the kinds are asked for.
type caRotation struct{ r *rotation }

var _ layer.Rotation = caRotation{}

func (caRotation) Kinds() []clustersecrets.RotationKind {
	return []clustersecrets.RotationKind{clustersecrets.RotateOSCA}
}

func (caRotation) CheckStart(context.Context, layer.Rotating, clustersecrets.RotationKind) error {
	return nil
}

func (c caRotation) ApplyRotation(ctx context.Context, _ layer.Rotating, _ clustersecrets.RotationKind, phase clustersecrets.Phase) error {
	return c.r.applyOSCA(ctx, phase)
}

// PausesAfter stops the OS CA's rotation after its accept phase, so client files trust the new
// OS CA before it issues the nodes' certificates, and after its refresh.
func (caRotation) PausesAfter(_ clustersecrets.RotationKind, phase clustersecrets.Phase) bool {
	return phase == clustersecrets.PhaseAccept || phase == clustersecrets.PhaseRefresh
}

func (c caRotation) Paused(_ context.Context, _ layer.Rotating, rot clustersecrets.Rotation) error {
	r := c.r
	if rot.Phase == clustersecrets.PhaseAccept {
		r.say("Every node trusts the old and the new OS CA. Issue new client files now with chalkctl config new: they carry both OS CAs and work throughout the rotation and after it. Client files from before the rotation stop verifying the nodes from the switch on and are refused at the finish. Then continue with chalkctl rotate %s --resume, which makes the new OS CA issue.", rot.Kind)
		return nil
	}
	r.say("Every node serves a certificate of the new node CA and trusts the old and the new OS CA. Client files from before the rotation cannot verify the nodes any more and are refused after the finish: issue new ones with chalkctl config new. Build images and installer media again from %s; older ones trust the old OS CA alone, and installing from them fails. Then remove the old OS CA with chalkctl rotate %s --finish.", r.publicFile(), rot.Kind)
	return nil
}

func (caRotation) FinishGuard(context.Context, layer.Rotating, clustersecrets.RotationKind, bool) error {
	return nil
}

// Finished: what a client trusts is in its file, which the finish cannot reach.
func (caRotation) Finished(clustersecrets.RotationKind) (string, string) {
	return "every node trusts the new value alone", "Client files issued during the rotation still trust the old OS CA besides the new one when they verify the nodes, so whoever holds the old OS CA's key could pose as a node to them; issue them again with chalkctl config new."
}

// applyOSCA runs a phase of the OS CA's rotation: nodes trust both OS CAs, the nodes that hold
// the node CA get the new one, every node a certificate of it, and finally nodes trust the new OS
// CA alone.
func (r *rotation) applyOSCA(ctx context.Context, phase clustersecrets.Phase) error {
	switch phase {
	case clustersecrets.PhaseAccept, clustersecrets.PhaseFinish:
		bundle := r.file.secrets.OSCABundle()
		want, err := pki.Fingerprints(bundle)
		if err != nil {
			return err
		}
		return layer.EachNode(r.allNodes(), func(name string) error {
			if err := r.deliver(ctx, name, &nodev1.ApplyConfigRequest{Trust: []byte(bundle)}); err != nil {
				return err
			}
			if err := layer.WaitStatus(ctx, r, name, "to trust the OS CAs", func(st *nodev1.StatusResponse) error {
				return layer.Trusts(st, client.NameOSCA, want, "")
			}); err != nil {
				return err
			}
			r.say("  %s trusts the %s of the secrets file", name, layer.Count(len(want), "OS CA"))
			return nil
		})
	case clustersecrets.PhaseSwitch:
		return r.switchNodeCA(ctx)
	case clustersecrets.PhaseRefresh:
		return r.refreshNodeCertificates(ctx)
	}
	return nil
}

// switchNodeCA delivers the secrets file's node CA to the nodes that hold it and waits until each
// does.
func (r *rotation) switchNodeCA(ctx context.Context) error {
	nodeCA, err := layer.FingerprintOf(r.file.secrets.NodeCA.Certificate)
	if err != nil {
		return err
	}
	holders, err := nodeCAHolders(r.cluster)
	if err != nil {
		return err
	}
	holders = slices.DeleteFunc(holders, func(name string) bool { return r.revoked[name] })
	return layer.EachNode(holders, func(name string) error {
		if err := r.deliverPayloads(ctx, name); err != nil {
			return err
		}
		if err := layer.WaitStatus(ctx, r, name, "to hold the new node CA", func(st *nodev1.StatusResponse) error {
			if c := layer.CertificateOf(st, client.NameNodeCA); c == nil || c.Fingerprint != nodeCA {
				return errors.New("holds another node CA")
			}
			return nil
		}); err != nil {
			return err
		}
		r.say("  %s renews node certificates with the new node CA", name)
		return nil
	})
}

// refreshNodeCertificates gives every node a certificate of the secrets file's node CA, unless
// it serves one already.
func (r *rotation) refreshNodeCertificates(ctx context.Context) error {
	s := r.file.secrets
	nodeCA, err := layer.FingerprintOf(s.NodeCA.Certificate)
	if err != nil {
		return err
	}
	return layer.EachNode(r.allNodes(), func(name string) error {
		// A node renewed since, or in an earlier run, keeps its certificate.
		if st, err := r.status(ctx, name); err == nil && layer.IssuedBy(st, client.NameNode, nodeCA) == nil {
			r.say("  %s serves a certificate of the new node CA already", name)
			return nil
		}
		names, err := r.cluster.certNames(name)
		if err != nil {
			return err
		}
		cert, err := pki.IssueNode(s.NodeCA, names, r.a.now())
		if err != nil {
			return err
		}
		if err := r.deliver(ctx, name, &nodev1.ApplyConfigRequest{NodeCertificate: []byte(cert.Certificate), NodeKey: []byte(cert.Key)}); err != nil {
			return err
		}
		if err := layer.WaitStatus(ctx, r, name, "to serve a certificate of the new node CA", func(st *nodev1.StatusResponse) error {
			return layer.IssuedBy(st, client.NameNode, nodeCA)
		}); err != nil {
			return err
		}
		r.say("  %s serves a certificate of the new node CA", name)
		return nil
	})
}
