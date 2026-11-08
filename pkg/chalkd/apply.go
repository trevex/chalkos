package chalkd

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
)

// applyRequest is an ApplyConfig request once its config is translated: the identity the node
// runs from now on, empty to keep its own; the config set it keeps, checked against the running
// image's schemas; and the rest of the request.
type applyRequest struct {
	identity string
	next     slot.Stored
	msg      *nodev1.ApplyConfigRequest
	// try is how long the config is on trial; zero applies it for good.
	try time.Duration
}

func (s *Server) ApplyConfig(ctx context.Context, req *connect.Request[nodev1.ApplyConfigRequest]) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A config on trial is confirmed or rolled back before the node takes another delivery.
	if err := s.refuseOnTrial("another delivery", false); err != nil {
		return nil, err
	}
	try, err := trialOf(req.Msg)
	if err != nil {
		return nil, err
	}
	// An upgrade writes its image's slot, checked against the machine, before the image is
	// staged, so a delivery in between would find no staged image to write the slot of. TryLock
	// never waits: the upgrade, an image rollback and a trial's deadline hold upgrading while they
	// take s.mu.
	// An upgrade reads and removes the pending config, so storing or dropping it waits for none
	// either.
	if req.Msg.Config != "" || req.Msg.DiscardPending {
		if !s.upgrading.TryLock() {
			return nil, layer.Errorf(connect.CodeAborted, "an upgrade, an image rollback or the rollback at a trial's deadline is running; apply the config once it ended")
		}
		defer s.upgrading.Unlock()
	}
	if req.Msg.DiscardPending {
		return s.discardPending(req.Msg)
	}
	if req.Msg.Staged {
		return s.applyStaged(ctx, req.Msg)
	}
	r := applyRequest{msg: req.Msg, try: try}
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
		r.identity, r.next = string(c.Identity), c
	}
	if req.Msg.DryRun {
		return s.dryRun(ctx, r)
	}
	if try > 0 {
		return s.applyTrial(ctx, r)
	}
	return s.apply(ctx, r)
}

// dryRun checks an ApplyConfig request as apply does, the trial's conditions with try, and
// answers with what the delivery would do in place of doing it, the staged image's slot or the
// pending config among it; the caller holds s.mu and, for a request with config, s.upgrading.
func (s *Server) dryRun(ctx context.Context, r applyRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	d, err := s.planDelivery(ctx, r)
	if err != nil {
		return nil, err
	}
	defer d.staged.Discard()
	second, err := s.planSecond(ctx, d)
	if err != nil {
		return nil, err
	}
	o, warnings, err := s.outcome(ctx, d.fx, d.staged.States(), d.msg.FallbackSecret)
	if err != nil {
		return nil, err
	}
	p := check.PlanOf(d.fx.checkEffects(), o)
	plan := &nodev1.Plan{Outputs: p.Outputs}
	if !d.keep {
		plan.Version, plan.Replaces = d.stored.Version, d.replaced
	}
	for _, e := range p.Effects {
		plan.Effects = append(plan.Effects, &nodev1.PlannedEffect{Name: e.Name, FiredBy: e.FiredBy, Actions: e.Actions})
	}
	resp := &nodev1.ApplyConfigResponse{Plan: plan, Warnings: warnings}
	for _, c := range d.machine.changes {
		resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
	}
	if r.try > 0 {
		if err := checkSecondTrial(second); err != nil {
			return nil, err
		}
	}
	second.plan(resp)
	return connect.NewResponse(resp), nil
}

// apply delivers an ApplyConfig request whose config is translated: to the running image, then
// to the staged image's slot or the pending config, as planSecond plans it before anything
// changes. The caller holds s.mu and, for a request with config, s.upgrading.
func (s *Server) apply(ctx context.Context, r applyRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	d, err := s.planDelivery(ctx, r)
	if err != nil {
		return nil, err
	}
	defer d.staged.Discard()
	second, err := s.planSecond(ctx, d)
	if err != nil {
		s.refused(d.fx)
		return nil, err
	}
	resp, err := d.commit(ctx)
	if err != nil {
		return nil, err
	}
	if err := second.write(ctx, s, d.stored.Version, d.msg.FallbackSecret, resp.Msg); err != nil {
		return nil, err
	}
	return resp, nil
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
	// replaced is the version of the config the slot held, which the delivery replaces; unset
	// with keep.
	replaced string
	// identity is the identity the node runs once the delivery is applied.
	identity delivered
	// osCA is the OS CAs the node trusts once the delivery is applied.
	osCA string
	// commits are what the image's layers change, as they planned the delivery.
	commits []layer.Commit
	// held is set for a delivery of the config the slot runs already, which writes the slot no
	// more: it applies the config and takes the effects, as the end of a trial's rollback does.
	held bool
	// started is set once commit recorded the attempt and may change the node; a commit that
	// failed without it changed nothing.
	started bool
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
		return nil, layer.Errorf(connect.CodeInvalidArgument, "the request delivers no config, layer payload, node certificate or OS CAs")
	}
	// A node certificate is checked against the OS CAs the node trusts when it arrives.
	if len(m.Trust) > 0 && len(m.NodeCertificate) > 0 {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "deliver the OS CAs and a node certificate one after the other")
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
			return nil, layer.Errorf(connect.CodeFailedPrecondition, "STATE holds no config for the running image, so the node has no identity to keep; deliver config with chalkctl apply-config")
		}
		data = string(prev.Identity)
	} else {
		d.dir, d.stored, d.replaced = dir, r.next, prev.Version
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
	// What the rules, validators, effects and outputs read of the delivery and of the running slot.
	// A delivery without config keeps the running slot's documents.
	old := config.Redact(prev.Documents)
	next := old
	if !d.keep {
		next = config.Redact(r.next.Documents)
	}
	// The running image checks the delivery in the stages of its checker, between the checks of
	// the machine: its rules and validators here, its renderers last.
	img, err := s.runningImage()
	if err != nil {
		return nil, err
	}
	if !d.keep {
		// The image's rules compare the config with the running slot's and the storage STATE
		// records, and read the node's facts. A missing or mistyped fallback secret is refused
		// before the node trusts the delivered OS CAs.
		f := s.factsFor(ctx, next, old, m.FallbackSecret)
		if err := img.Rules(rules.Input{Documents: next, Old: old, Node: name, Facts: f}); err != nil {
			return nil, s.elsewhere(err, d.identity.section, nil)
		}
		// The image's validators check what its rules do not; a delivery without config changes no
		// document, so it runs none.
		if err := img.Validate(ctx, name, next); err != nil {
			return nil, err
		}
	}
	if err := s.checkNodeCertificate(m.NodeCertificate, m.NodeKey); err != nil {
		return nil, err
	}
	if !d.keep {
		if d.machine, err = s.machineChanges(d.identity.section); err != nil {
			return nil, err
		}
	}
	// A config on trial must be one its rollback undoes whole.
	if r.try > 0 {
		if err := checkTrial(found, d.replaced, r.next.Version, d.machine); err != nil {
			return nil, err
		}
	}
	// The effects compare the delivery with the running slot, the facts once it is applied with
	// the facts loaded now, and the outputs rendered from it with those the node serves. Both read
	// the documents with the schemas of the image whose slot holds them, the running image's.
	if d.fx, err = s.effectsOf(ctx, name, next, old, img.Schemas, m.FallbackSecret, m.DryRun); err != nil {
		return nil, err
	}
	// Every output renders from the delivered documents and passes its check, so a delivery a
	// service would reject is refused whole. A delivery without config changes no document, so an
	// output that fails does not refuse it.
	if d.staged, err = s.renderOutputs(ctx, img, name, next, !d.keep, m.DryRun); err != nil {
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
// the failed attempt wrote. The baseline records what the delivery applies before its first
// change, so another delivery, even one of the config the node ran before, takes the effects that
// undo it, whether the delivery failed or chalkd stopped part way. storage.json is the machine's
// rather than an image's: applyStorage records the section once its additive changes are made, so
// a retry makes none twice.
func (d *delivery) commit(ctx context.Context) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	s, m := d.s, d.msg
	if err := s.recordAttempt(d.fx, d.staged.States()); err != nil {
		s.refused(d.fx)
		return nil, err
	}
	d.started = true
	if len(m.Trust) > 0 {
		if err := s.Certificate.ReplaceTrust(d.osCA, peerOf(ctx)...); err != nil {
			if isRefusedTrust(err) {
				d.started = false
				s.refused(d.fx)
			}
			return nil, err
		}
		log.Printf("trusting %s from now on", count(strings.Count(d.osCA, "-----BEGIN CERTIFICATE-----"), "OS CA"))
	}
	if !d.keep {
		if err := s.applyMachine(ctx, d.machine, m.FallbackSecret); err != nil {
			return nil, err
		}
		// networkd and chrony read the identity's units and time servers again as their effects say.
		if err := s.Loader.Apply(d.identity.data); err != nil {
			return nil, fmt.Errorf("apply the identity: %w", err)
		}
		for _, c := range d.commits {
			if c.Config == nil {
				continue
			}
			if err := c.Config(); err != nil {
				s.failedPartWay(ctx, d.fx, m.FallbackSecret)
				return nil, layer.Errorf(connect.CodeInternal, "%w", err)
			}
		}
	}
	if len(m.NodeCertificate) > 0 {
		if err := s.Certificate.Replace(string(m.NodeCertificate), string(m.NodeKey), time.Now()); err != nil {
			s.failedPartWay(ctx, d.fx, m.FallbackSecret)
			return nil, layer.Errorf(connect.CodeInvalidArgument, "%w", err)
		}
		log.Printf("serving a delivered node certificate; it expires %s", s.Certificate.Current().Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	for _, c := range d.commits {
		if c.Payloads == nil {
			continue
		}
		if err := c.Payloads(s.Paths.StateDir); err != nil {
			s.failedPartWay(ctx, d.fx, m.FallbackSecret)
			return nil, layer.Errorf(connect.CodeInternal, "%w", err)
		}
	}
	// The documents and the identity are written in one rename.
	if !d.keep && !d.held {
		d.stored.Identity = d.identity.data
		if err := slot.WriteKeepingPrevious(d.dir, d.stored); err != nil {
			s.failedPartWay(ctx, d.fx, m.FallbackSecret)
			return nil, fmt.Errorf("store the config: %w", err)
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
