package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/strs"
)

// revocationLayer is a compiled plugin whose layer lists revoked nodes, with the layer's name.
type revocationLayer struct {
	name string
	layer.Revocations
}

// revocationLayers are the compiled plugins that implement Revocations, in the plugins' order.
func revocationLayers() []revocationLayer {
	var list []revocationLayer
	for _, p := range plugins {
		if r, ok := p.(layer.Revocations); ok {
			list = append(list, revocationLayer{p.Name(), r})
		}
	}
	return list
}

// refreshes are the revocations a command run with --ignore-revoked has the layers refresh once
// it handed out what it delivers, by layer name.
type refreshes map[string][]layer.Revocation

// revokedBefore reads, before a command run with --ignore-revoked hands names anything, which of
// them each layer lists as revoked; install is set for nodes about to be installed. A layer that
// cannot say reads its list again for each name once they were handed what they got.
func revokedBefore(ctx context.Context, nodes layer.Nodes, names []string, install bool) refreshes {
	rs := refreshes{}
	for _, l := range revocationLayers() {
		listed, err := l.RevokedAmong(ctx, nodes, names, install)
		if err != nil {
			listed = nil
			for _, name := range names {
				listed = append(listed, layer.Revocation{Node: name})
			}
		}
		rs[l.name] = listed
	}
	return rs
}

// withRecorded adds to rs the revocations the secrets file records as not refreshed, each once.
func (rs refreshes) withRecorded(recorded []clustersecrets.Unrefreshed) refreshes {
	all := refreshes{}
	for name, list := range rs {
		all[name] = slices.Clone(list)
	}
	for _, u := range recorded {
		list := all[u.Layer]
		i := slices.IndexFunc(list, func(r layer.Revocation) bool { return r.Node == u.Node })
		switch {
		case i < 0:
			all[u.Layer] = append(list, layer.Revocation{Node: u.Node, Listed: u.Listed})
		case u.Listed:
			// The node was listed as revoked when an earlier command handed it something.
			list[i].Listed, list[i].Later = true, false
		}
	}
	return all
}

// refreshRevocations has each layer refresh its revocations in rs and those recorded as not
// refreshed, through nodes, and writes what each refreshed to stdout. A revocation Later is left
// without a try, with a warning. It returns the revocations tried, each with the time recorded
// gives it, and those left, as the secrets file records them, and the layers' errors, which say
// what to run.
func (a *app) refreshRevocations(ctx context.Context, nodes layer.Nodes, rs refreshes, recorded []clustersecrets.Unrefreshed) (tried, left []clustersecrets.Unrefreshed, err error) {
	rs = rs.withRecorded(recorded)
	var errs []error
	for _, l := range revocationLayers() {
		var now []layer.Revocation
		for _, r := range rs[l.name] {
			u := clustersecrets.Unrefreshed{Layer: l.name, Node: r.Node, Listed: r.Listed}
			if i := slices.IndexFunc(recorded, func(v clustersecrets.Unrefreshed) bool { return v.Layer == u.Layer && v.Node == u.Node }); i >= 0 {
				u.Recorded = recorded[i].Recorded
			}
			tried = append(tried, u)
			if r.Later {
				left = append(left, u)
				fmt.Fprintf(a.stderr, "chalkctl: warning: whether %s is revoked is read once it is back from its install: the secrets file records its revocation as not refreshed until then, and chalkctl refreshes it before it prunes the revoked nodes\n", r.Node)
				continue
			}
			now = append(now, r)
		}
		if len(now) == 0 {
			continue
		}
		lines, failed, err := l.RefreshRevoked(ctx, nodes, now)
		for _, line := range lines {
			fmt.Fprintln(a.stdout, line)
		}
		for _, r := range failed {
			left = append(left, clustersecrets.Unrefreshed{Layer: l.name, Node: r.Node, Listed: r.Listed})
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return tried, left, errors.Join(errs...)
}

// refreshAndRecord refreshes rs and the revocations recorded as not refreshed, through nodes,
// and records in the secrets file sf names which are left. It fails while one is left.
func (a *app) refreshAndRecord(ctx context.Context, sf layer.SecretsFlags, nodes layer.Nodes, rs refreshes, recorded []clustersecrets.Unrefreshed) error {
	tried, left, err := a.refreshRevocations(ctx, nodes, rs, recorded)
	if len(recorded) == 0 && len(left) == 0 {
		return err
	}
	if rerr := a.recordRefreshes(ctx, sf, tried, left); rerr != nil {
		if len(left) == 0 {
			// Every revocation is refreshed; the next retry finds them refreshed and drops them.
			fmt.Fprintf(a.stderr, "chalkctl: warning: %v; a refresh retried with the secrets file clears the record\n", rerr)
			return nil
		}
		return errors.Join(err, rerr)
	}
	if err != nil {
		return fmt.Errorf("%w\n%s", err, recordedNote)
	}
	return nil
}

// recordedNote says what the record of the revocations not refreshed does.
const recordedNote = "the secrets file records the revocations not refreshed; chalkctl prunes none of a layer's revoked nodes while it records one, and refreshes them again first"

// recordRefreshes records in the secrets file sf names that the revocations tried were refreshed
// but for those left, keeping what other commands recorded since the record was read, which the
// times tried carries tell apart. The secrets file
// from standard input records nothing: it fails while one is left, or while it records one tried.
func (a *app) recordRefreshes(ctx context.Context, sf layer.SecretsFlags, tried, left []clustersecrets.Unrefreshed) error {
	if sf.Path == "-" {
		if len(left) > 0 {
			return errors.New("the secrets file from standard input cannot record the revocations not refreshed; refresh them before the revoked nodes are pruned")
		}
		s, err := a.loadSecrets(ctx, sf)
		if err != nil {
			return err
		}
		var kept []string
		for _, u := range tried {
			if slices.ContainsFunc(s.Unrefreshed, func(r clustersecrets.Unrefreshed) bool { return r.Layer == u.Layer && r.Node == u.Node }) {
				kept = append(kept, u.Node)
			}
		}
		if len(kept) == 0 {
			return nil
		}
		return fmt.Errorf("the secrets file from standard input cannot be written back, so its record of the revocations not refreshed still holds %s", strs.And(kept))
	}
	file, err := a.openSecrets(ctx, sf, changeFlags{})
	if err != nil {
		return fmt.Errorf("record the revocations not refreshed: %w", err)
	}
	defer file.Close()
	// The record is no part of the public half.
	file.publicOut = ""
	s := file.secrets
	if !s.RecordRefreshes(tried, left, time.Now()) {
		return nil
	}
	if err := a.writeSecretsFile(file, s); err != nil {
		return fmt.Errorf("record the revocations not refreshed: %w", err)
	}
	return nil
}

// RetryRefresh refreshes the layer's revocations the secrets file records as not refreshed, only
// node's when node is set.
func (h host) RetryRefresh(ctx context.Context, f layer.NodeFlags, nodes layer.Nodes, name, node string) error {
	recorded, err := h.recorded(ctx, f, func(u clustersecrets.Unrefreshed) bool { return u.Layer == name && (node == "" || u.Node == node) })
	if err != nil || len(recorded) == 0 {
		return err
	}
	return h.a.refreshAndRecord(ctx, f.Secrets, nodes, refreshes{}, recorded)
}

// Unrevoked drops the layer's revocation of node from those the secrets file records as not
// refreshed.
func (h host) Unrevoked(ctx context.Context, f layer.NodeFlags, name, node string) error {
	recorded, err := h.recorded(ctx, f, func(u clustersecrets.Unrefreshed) bool { return u.Layer == name && u.Node == node })
	if err != nil || len(recorded) == 0 {
		return err
	}
	return h.a.recordRefreshes(ctx, f.Secrets, recorded, nil)
}

// recorded are the revocations the secrets file records as not refreshed that keep accepts; none
// when the command authenticates with a client file.
func (h host) recorded(ctx context.Context, f layer.NodeFlags, keep func(clustersecrets.Unrefreshed) bool) ([]clustersecrets.Unrefreshed, error) {
	if f.Secrets.Path == "" || f.Config != "" {
		return nil, nil
	}
	s, err := h.a.loadSecrets(ctx, f.Secrets)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(slices.Clone(s.Unrefreshed), func(u clustersecrets.Unrefreshed) bool { return !keep(u) }), nil
}
