package chalkd

import (
	"context"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/schema"
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
	d, err := s.planDelivery(ctx, r)
	if err != nil {
		return nil, err
	}
	defer d.staged.Discard()
	return d.commit(ctx)
}

// delivery is an ApplyConfig request that passed the checks made before the node changes; the
// trust's own checks come first in commit.
type delivery struct {
	s   *Server
	msg *nodev1.ApplyConfigRequest
	// keep is set for a request without config: the node keeps its identity and documents.
	keep bool
	// dir is the running image's config slot, and stored the config written to it; unset with keep.
	dir    string
	stored slot.Stored
	// identity is the identity the node runs once the delivery is applied.
	identity delivered
	// osCA is the OS CAs the node trusts once the delivery is applied.
	osCA string
	// commits are what the image's layers change, as they planned the delivery.
	commits []layer.Commit
	// machine is what the config adds to the machine's storage; empty with keep.
	machine machineChange
	fx      *pendingEffects
	staged  *outputs.Staged
}

// planDelivery checks an ApplyConfig request against the node before anything changes, so a
// refused request changes nothing. The caller discards the staged outputs of the delivery.
func (s *Server) planDelivery(ctx context.Context, r applyRequest) (*delivery, error) {
	m := r.msg
	// Without config the node keeps its own, which the layers' payloads are checked against.
	d := &delivery{s: s, msg: m, keep: r.identity == ""}
	if d.keep && len(m.Layers) == 0 && len(m.NodeCertificate) == 0 && len(m.Trust) == 0 {
		return nil, failed(connect.CodeInvalidArgument, "the request delivers no config, layer payload, node certificate or OS CAs")
	}
	// A node certificate is checked against the OS CAs the node trusts when it arrives.
	if len(m.Trust) > 0 && len(m.NodeCertificate) > 0 {
		return nil, failed(connect.CodeInvalidArgument, "deliver the OS CAs and a node certificate one after the other")
	}
	var err error
	if d.osCA, err = s.trustFor(ctx, m.Trust); err != nil {
		return nil, err
	}
	dir, prev, found, err := s.runningSlot()
	if err != nil {
		return nil, err
	}
	data := r.identity
	if d.keep {
		if !found {
			return nil, failed(connect.CodeFailedPrecondition, "STATE holds no config for the running image, so the node has no identity to keep; deliver config with chalkctl apply-config")
		}
		data = string(prev.Identity)
	} else {
		d.dir, d.stored = dir, r.next
	}
	if d.identity, err = s.parseIdentity(data); err != nil {
		return nil, err
	}
	// The rules, the effects and the renderers see the node by the name its node certificate gives.
	name, err := s.name()
	if err != nil {
		return nil, err
	}
	// The layers check their payloads and what the config gives them before the rules.
	var docs config.Documents
	if !d.keep {
		docs = r.next.Documents
	}
	if d.commits, err = layer.Plan(ctx, layer.Active[layer.Delivery](s.Plugins), m.Layers, layer.Planned{
		Identity: d.identity.data, Documents: docs, OSCA: d.osCA, Trust: len(m.Trust) > 0,
		ForceRenewals: s.RenewOnApplyConfig,
	}); err != nil {
		return nil, err
	}
	// The delivery's facts, loaded once for the running slot's check and the staged slot's.
	var loaded *facts.Loader
	if !d.keep {
		// The image's rules compare the config with the running slot's and the storage STATE
		// records, and read the node's facts. A missing or mistyped fallback secret is refused
		// before the node trusts the delivered OS CAs.
		set, err := compileRunningRules(r.schemas)
		if err != nil {
			return nil, err
		}
		if loaded, err = s.factsFor(ctx, r.next.Documents, prev.Documents, m.FallbackSecret); err != nil {
			return nil, err
		}
		if err := s.checkRules(set, name, r.next.Documents, prev.Documents, loaded); err != nil {
			return nil, s.elsewhere(err, d.identity.section, nil)
		}
	}
	if err := s.checkNodeCertificate(m.NodeCertificate, m.NodeKey); err != nil {
		return nil, err
	}
	// The staged image boots onto the machine's storage too, so the config's changes to it are
	// checked against that image's config as well.
	if !d.keep {
		if d.machine, err = s.machineChanges(d.identity.section); err != nil {
			return nil, err
		}
		if len(d.machine.changes) > 0 {
			if err := s.checkStaged(r.schemas, name, r.next.Documents, d.identity.section, loaded); err != nil {
				return nil, err
			}
		}
	}
	// The effects compare the delivery with the running slot, the facts once it is applied with
	// the facts loaded now, and the outputs rendered from it with those the node serves.
	next := prev.Documents
	if !d.keep {
		next = r.next.Documents
	}
	if d.fx, err = s.effectsOf(ctx, name, next, prev.Documents, r.schemas, m.FallbackSecret); err != nil {
		return nil, err
	}
	// Every output renders from the delivered documents and passes its check, so a delivery a
	// service would reject is refused whole. A delivery without config changes no document, so an
	// output that fails does not refuse it.
	if d.staged, err = s.renderOutputs(ctx, name, next, r.schemas, !d.keep); err != nil {
		s.refused(d.fx)
		return nil, err
	}
	return d, nil
}

// commit applies a planned delivery. The changes come in this order:
//  1. the OS CAs, which the layers checked against what they keep;
//  2. the machine's storage, the identity and what the layers derive from the config;
//  3. the node certificate and the layers' payloads;
//  4. the running slot, last, which keeps the config it replaces as the image's previous one;
//  5. the effects.
//
// Until the slot is written, STATE holds the identity and the documents the node ran before, which
// a boot applies whole. A retry applies the identity and the granted units' config again, and its
// effects compare with the baseline effectsOf recorded, so it restarts what they ask for whatever
// the failed attempt wrote. storage.json is the machine's rather than an image's: applyStorage
// records the section once its additive changes are made, so a retry makes none twice.
func (d *delivery) commit(ctx context.Context) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	s, m := d.s, d.msg
	if len(m.Trust) > 0 {
		if err := s.Certificate.ReplaceTrust(d.osCA, peerOf(ctx)...); err != nil {
			if isRefusedTrust(err) {
				s.refused(d.fx)
				return nil, failed(connect.CodeInvalidArgument, "%v", err)
			}
			return nil, failed(connect.CodeInternal, "%v", err)
		}
		log.Printf("trusting %s from now on", count(strings.Count(d.osCA, "-----BEGIN CERTIFICATE-----"), "OS CA"))
	}
	if !d.keep {
		if err := s.applyMachine(ctx, d.machine, m.FallbackSecret); err != nil {
			return nil, err
		}
		// networkd and chrony read the identity's units and time servers again as their effects say.
		if err := s.Loader.Apply(d.identity.data); err != nil {
			return nil, failed(connect.CodeInternal, "apply the identity: %v", err)
		}
		for _, c := range d.commits {
			if c.Config == nil {
				continue
			}
			if err := c.Config(); err != nil {
				s.failedPartWay(ctx, d.fx, m.FallbackSecret)
				return nil, failed(connect.CodeInternal, "%v", err)
			}
		}
	}
	if len(m.NodeCertificate) > 0 {
		if err := s.Certificate.Replace(string(m.NodeCertificate), string(m.NodeKey), time.Now()); err != nil {
			s.failedPartWay(ctx, d.fx, m.FallbackSecret)
			return nil, failed(connect.CodeInvalidArgument, "%v", err)
		}
		log.Printf("serving a delivered node certificate; it expires %s", s.Certificate.Current().Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	for _, c := range d.commits {
		if c.Payloads == nil {
			continue
		}
		if err := c.Payloads(s.Paths.StateDir); err != nil {
			s.failedPartWay(ctx, d.fx, m.FallbackSecret)
			return nil, failed(connect.CodeInternal, "%v", err)
		}
	}
	// The documents and the identity are written in one rename.
	if !d.keep {
		d.stored.Identity = d.identity.data
		if err := slot.WriteKeepingPrevious(d.dir, d.stored); err != nil {
			s.failedPartWay(ctx, d.fx, m.FallbackSecret)
			return nil, failed(connect.CodeInternal, "store the config: %v", err)
		}
	}
	restarted, warnings, err := s.consume(ctx, d.fx, d.staged, !d.keep, m.FallbackSecret)
	if err != nil {
		return nil, err
	}
	// A delivery without config, as a rotation's, renews nothing: forced renewals would restart
	// services on top of the restarts the rotation waits for. Renewals come after the loops started
	// again, which would end a renewal under way. The layers plan their own renewals alike.
	if s.RenewOnApplyConfig && !d.keep && s.Renewal != nil && s.Renewal.Renew != nil {
		log.Print("renewing the node certificate, as this test image does after each config")
		s.Renewal.Force()
	}
	for _, c := range d.commits {
		if c.Applied != nil {
			c.Applied()
		}
	}
	resp := &nodev1.ApplyConfigResponse{RestartedUnits: restarted, Warnings: warnings}
	for _, c := range d.machine.changes {
		resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
	}
	return connect.NewResponse(resp), nil
}
