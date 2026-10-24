package chalkd

import (
	"context"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
)

// applyRequest is an ApplyConfig request once its config is translated: the identity the node
// runs from now on, empty to keep its own; the config set it keeps and the schemas that set was
// checked against; and the rest of the request.
type applyRequest struct {
	identity string
	next     slot.Stored
	schemas  *schema.Schemas
	msg      *nodev1.ApplyConfigRequest
}

func (s *Server) ApplyConfig(ctx context.Context, req *connect.Request[nodev1.ApplyConfigRequest]) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An upgrade writes its image's slot, checked against the machine, before the image is
	// staged, so a machine change in between would escape checkStaged. TryLock never waits: the
	// upgrade holds upgrading while it takes s.mu.
	if req.Msg.Config != "" {
		if !s.upgrading.TryLock() {
			return nil, failed(connect.CodeAborted, "an upgrade is running, which writes the node's config too; apply the config once it ended")
		}
		defer s.upgrading.Unlock()
	}
	if req.Msg.Staged {
		return s.applyStaged(ctx, req.Msg)
	}
	r := applyRequest{msg: req.Msg}
	if req.Msg.Config != "" {
		name, err := s.name()
		if err != nil {
			return nil, err
		}
		t, schemas, err := s.runningTarget()
		if err != nil {
			return nil, err
		}
		c, err := configure(name, req.Msg.Config, schemas, t)
		if err != nil {
			return nil, err
		}
		r.identity, r.next, r.schemas = string(c.Identity), c, schemas
	}
	return s.apply(ctx, r)
}

// apply delivers an ApplyConfig request whose config is translated; the caller holds s.mu.
func (s *Server) apply(ctx context.Context, r applyRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	m := r.msg
	// Without config the node keeps its own, which the share is checked against.
	keep := r.identity == ""
	if keep && len(m.KubernetesShare) == 0 && len(m.NodeCertificate) == 0 && len(m.Trust) == 0 {
		return nil, failed(connect.CodeInvalidArgument, "the request delivers no config, share, node certificate or OS CAs")
	}
	// A node certificate is checked against the OS CAs the node trusts when it arrives.
	if len(m.Trust) > 0 && len(m.NodeCertificate) > 0 {
		return nil, failed(connect.CodeInvalidArgument, "deliver the OS CAs and a node certificate one after the other")
	}
	osCA, err := s.trustFor(ctx, m.Trust)
	if err != nil {
		return nil, err
	}
	dir, prev, found, err := s.runningSlot()
	if err != nil {
		return nil, err
	}
	identity := r.identity
	if keep {
		if !found {
			return nil, failed(connect.CodeFailedPrecondition, "STATE holds no config for the running image, so the node has no identity to keep; deliver config with chalkctl apply-config")
		}
		identity = string(prev.Identity)
	}
	d, err := parseIdentity(identity)
	if err != nil {
		return nil, err
	}
	// The rules, the effects and the renderers see the node by the name its node certificate gives.
	name, err := s.name()
	if err != nil {
		return nil, err
	}
	// The cluster the config gives, checked before anything changes; nil on an image without
	// Kubernetes or without new config.
	var cluster *k8s.Cluster
	// The delivery's facts, loaded once for the running slot's check and the staged slot's.
	var delivered *facts.Loader
	if !keep {
		if s.Kubernetes != nil {
			if cluster, err = s.Loader.Cluster(r.next.Documents); err != nil {
				return nil, failed(connect.CodeInvalidArgument, "the config: %v", err)
			}
		}
		// The image's rules compare the config with the running slot's and the storage STATE
		// records, and read the node's facts, which the delivery loads once for this check and the
		// staged slot's, before anything changes: a missing or mistyped fallback secret is refused
		// before the node trusts the delivered OS CAs.
		set, err := compileRules(r.schemas, true)
		if err != nil {
			return nil, err
		}
		if delivered, err = s.factsFor(ctx, r.next.Documents, prev.Documents, m.FallbackSecret); err != nil {
			return nil, err
		}
		if err := s.checkRules(set, name, r.next.Documents, prev.Documents, delivered); err != nil {
			return nil, s.elsewhere(err, d.section, nil)
		}
	}
	share, err := s.kubernetesShare(d, m.KubernetesShare, osCA, cluster)
	if err != nil {
		return nil, err
	}
	if err := s.checkNodeCertificate(m.NodeCertificate, m.NodeKey); err != nil {
		return nil, err
	}
	// The changes the config makes to the machine's storage, which the staged image boots onto
	// too, are checked against that image's config before the node trusts new OS CAs or changes
	// anything else, so a refused request changes nothing.
	var mc machineChange
	if !keep {
		if mc, err = s.machineChanges(d.section); err != nil {
			return nil, err
		}
		if len(mc.changes) > 0 {
			if err := s.checkStaged(r.schemas, name, r.next.Documents, d.section, delivered); err != nil {
				return nil, err
			}
		}
	}
	// The effects compare the delivery with the running slot, the facts as they are once it is
	// applied with the facts as they were, which load before anything changes, and the outputs
	// rendered from it with those the node serves.
	next := prev.Documents
	if !keep {
		next = r.next.Documents
	}
	fx, err := s.effectsOf(ctx, name, next, prev.Documents, r.schemas, m.FallbackSecret)
	if err != nil {
		return nil, err
	}
	// Every output renders from the delivered documents and passes its check before anything
	// changes, so a delivery a service would reject is refused whole. A delivery without config
	// changes no document, so an output that fails does not refuse it.
	staged, err := s.renderOutputs(ctx, name, next, r.schemas, !keep)
	if err != nil {
		s.refused(fx)
		return nil, err
	}
	defer staged.Discard()
	if len(m.Trust) > 0 {
		if share == nil {
			// A control plane keeps renewing node certificates with the node CA it holds.
			if err := s.checkKeptNodeCA(osCA); err != nil {
				s.refused(fx)
				return nil, err
			}
		}
		if err := s.Certificate.ReplaceTrust(osCA, peerOf(ctx)...); err != nil {
			if isRefusedTrust(err) {
				s.refused(fx)
				return nil, failed(connect.CodeInvalidArgument, "%v", err)
			}
			return nil, failed(connect.CodeInternal, "%v", err)
		}
		log.Printf("trusting %s from now on", count(strings.Count(osCA, "-----BEGIN CERTIFICATE-----"), "OS CA"))
	}
	var changes []storage.Change
	if !keep {
		changes = mc.changes
		if err := s.applyMachine(ctx, mc, m.FallbackSecret); err != nil {
			return nil, err
		}
		if err := s.applyIdentity(d.data); err != nil {
			return nil, err
		}
		if cluster != nil {
			if err := s.Loader.WriteCluster(cluster); err != nil {
				s.failedPartWay(ctx, fx, m.FallbackSecret)
				return nil, failed(connect.CodeInternal, "write the Kubernetes cluster: %v", err)
			}
		}
	}
	if len(m.NodeCertificate) > 0 {
		if err := s.Certificate.Replace(string(m.NodeCertificate), string(m.NodeKey), time.Now()); err != nil {
			s.failedPartWay(ctx, fx, m.FallbackSecret)
			return nil, failed(connect.CodeInvalidArgument, "%v", err)
		}
		log.Printf("serving a delivered node certificate; it expires %s", s.Certificate.Current().Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if share != nil {
		if err := install.WriteShare(s.Paths.StateDir, share); err != nil {
			s.failedPartWay(ctx, fx, m.FallbackSecret)
			return nil, failed(connect.CodeInternal, "record the Kubernetes share: %v", err)
		}
	}
	// The slot comes last, documents and identity in one rename: until it is written, STATE holds
	// the identity and the set the node ran before, which a boot applies whole, and a retry applies
	// the identity and the granted units' config again. Until a delivery is applied whole, its
	// effects compare with the baseline effectsOf recorded, so a retry restarts what they
	// ask for whatever the failed attempt wrote. storage.json, written before, is the machine's
	// rather than an image's: applyStorage records the section once its additive changes are made,
	// so a retry finds them recorded and makes none twice.
	if !keep {
		r.next.Identity = d.data
		if err := slot.Write(dir, r.next); err != nil {
			s.failedPartWay(ctx, fx, m.FallbackSecret)
			return nil, failed(connect.CodeInternal, "store the config: %v", err)
		}
	}
	restarted, warnings, err := s.consume(ctx, fx, staged, !keep, m.FallbackSecret)
	if err != nil {
		return nil, err
	}
	// Deliveries without config, as a rotation's, renew nothing: forced renewals would
	// restart the control plane's pods on top of the restarts the rotation waits for. They come
	// after the loops started again, which would end a renewal under way.
	if s.RenewOnApplyConfig && !keep {
		s.forceRenewals()
	}
	resp := &nodev1.ApplyConfigResponse{RestartedUnits: restarted, Warnings: warnings}
	for _, c := range changes {
		resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
	}
	return connect.NewResponse(resp), nil
}

// forceRenewals renews the node certificate and, on a control plane whose loop runs, its
// certificates at once.
func (s *Server) forceRenewals() {
	if s.Renewal != nil {
		log.Print("renewing the node certificate, as this test image does after each config")
		s.Renewal.Force()
	}
	if s.Kubernetes != nil {
		s.Kubernetes.mu.Lock()
		leaves := s.Kubernetes.leaves
		s.Kubernetes.mu.Unlock()
		if leaves != nil {
			log.Print("renewing the control plane's certificates, as this test image does after each config")
			leaves.Force()
		}
	}
}

// applyIdentity applies the identity data: the networkd units and the time servers it gives,
// which networkd and chrony read again as their effects say. The running slot records
// it, with the config it came from, once the delivery is applied.
func (s *Server) applyIdentity(data []byte) error {
	if err := s.Loader.Apply(data); err != nil {
		return failed(connect.CodeInternal, "apply the identity: %v", err)
	}
	return nil
}

// kubernetesShare validates a delivered share for the node the identity names, a node CA in it
// against the OS CAs osCA, and returns it as STATE keeps it, in its canonical encoding; nil when
// none was delivered. cluster is the one the config of the same request gives, nil without
// config, when the node's cluster file is read instead.
func (s *Server) kubernetesShare(d delivered, data []byte, osCA string, cluster *k8s.Cluster) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if s.Kubernetes == nil {
		return nil, failed(connect.CodeInvalidArgument, "the node's image has no Kubernetes, so it takes no Kubernetes share")
	}
	share, err := kpki.ParseShare(data)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	if cluster == nil {
		c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
		if err != nil {
			return nil, failed(connect.CodeInvalidArgument, "the node has no Kubernetes cluster: %v", err)
		}
		cluster = &c
	}
	c := cluster
	if share.Kind != c.Kind {
		return nil, failed(connect.CodeInvalidArgument, "the share is for a %s node, but the node's Node document gives it the type %s", share.Kind, c.Kind)
	}
	var nodeName string
	if d.kubernetes != nil {
		nodeName = d.kubernetes.NodeName
	}
	// A node must never run its kubelet with a certificate issued for another node.
	if err := share.ValidateFor(nodeName); err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	// Certificates of another node CA would never verify against the node's OS CA.
	if share.NodeCA != nil {
		if err := pki.ValidateNodeCA(*share.NodeCA, osCA); err != nil {
			return nil, failed(connect.CodeInvalidArgument, "the share's node CA: %v", err)
		}
	}
	canonical, err := share.Encode()
	if err != nil {
		return nil, failed(connect.CodeInternal, "encode the Kubernetes share: %v", err)
	}
	return canonical, nil
}
