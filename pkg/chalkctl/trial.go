package chalkctl

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

// minTrial and maxTrial bound --try. A shorter trial ends before the restarts and the health
// check a confirmation waits for, and the node refuses a longer one. Tests lower minTrial.
var minTrial = 10 * time.Second

const maxTrial = 24 * time.Hour

// trialEnd is how long chalkctl waits after a trial's deadline for the node to answer with the
// trial ended, and how long it tries to confirm a trial past its deadline whose rollback failed.
var trialEnd = 2 * time.Minute

// confirmTrial confirms the trial of config version on the node, which otherwise rolls back to
// config previous. Each attempt dials a new connection, so a confirmation proves that a client
// still reaches the node after the delivery, and the node confirms only once it finds itself
// healthy. Attempts end at deadline, chalkctl's reckoning of the node's own, which falls after
// it, and a trial not confirmed by then is waited for: the node rolls it back by itself. A
// deadline that passed already is of a trial whose rollback at the deadline failed, which the
// node still lets a confirmation end: its attempts end after trialEnd, and nothing rolls back
// when they fail. An answer that asking again does not change ends the attempts at once.
func (a *app) confirmTrial(ctx context.Context, t *target, version, previous string, deadline time.Time) error {
	late := !time.Now().Before(deadline)
	if late {
		deadline = time.Now().Add(trialEnd)
	}
	fmt.Fprintf(a.stdout, "%s: waiting for a new connection and the node's health to confirm config %s\n", t.name, version)
	var last error
	permanent := false
	err := layer.WaitFor(ctx, "", time.Until(deadline), a.poll(), func() error {
		err := confirmOnce(ctx, t, version, deadline)
		// An attempt the deadline cut short says less than the one before it.
		if err != nil && (last == nil || connect.CodeOf(err) != connect.CodeDeadlineExceeded) {
			last = err
		}
		switch connect.CodeOf(err) {
		// UNAVAILABLE is an unhealthy node, a trial that changed while the node checked its
		// health or a node not reached, which may all pass. INTERNAL names its remedy, such as
		// a trial record the node cannot read, which only a rollback removes.
		case connect.CodeFailedPrecondition, connect.CodeInvalidArgument, connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInternal:
			permanent = true
			return layer.Permanent{Err: err}
		}
		return err
	})
	if err == nil {
		fmt.Fprintf(a.stdout, "%s keeps config %s: the trial is confirmed\n", t.name, version)
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	failure := fmt.Errorf("%s: config %s was not confirmed within its trial: %w", t.name, version, cmp.Or(last, err))
	if late || permanent {
		return failure
	}
	return a.trialRolledBack(ctx, t, previous, failure)
}

// confirmOnce asks the node to confirm the trial over a new connection, until the deadline.
func confirmOnce(ctx context.Context, t *target, version string, deadline time.Time) error {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ConfirmConfig(ctx, connect.NewRequest(&nodev1.ConfirmConfigRequest{Version: version}))
	return nodeError(err, t.name)
}

// trialRolledBack waits, over new connections, for the node to end the trial that chalkctl could
// not confirm for failure, says how it ended, and returns failure.
func (a *app) trialRolledBack(ctx context.Context, t *target, previous string, failure error) error {
	var st *nodev1.StatusResponse
	err := layer.WaitFor(ctx, "", trialEnd, a.poll(), func() error {
		conn, err := dialInstalled(t)
		if err != nil {
			return err
		}
		defer conn.Close()
		resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			return err
		}
		if tr := resp.Msg.Trial; tr != nil {
			if tr.Error != "" {
				return fmt.Errorf("its trial of config %s is still open: the rollback at the deadline failed: %s", tr.Version, tr.Error)
			}
			return fmt.Errorf("its trial of config %s is still open", tr.Version)
		}
		st = resp.Msg
		return nil
	})
	switch {
	case err != nil:
		return fmt.Errorf("%w; the node did not answer after the deadline either: %v; chalkctl status %s shows where it stands", failure, err, t.name)
	case st.ConfigVersion == previous:
		fmt.Fprintf(a.stdout, "%s rolled back to config %s at the trial's deadline\n", t.name, previous)
	default:
		fmt.Fprintf(a.stdout, "%s runs config %s\n", t.name, cmp.Or(st.ConfigVersion, "none"))
	}
	return failure
}

// confirmConfig confirms the node's open trial, as apply-config --try does without --no-confirm.
// The node keeps the config on trial, so no inventory needs to give it.
func (a *app) confirmConfig(ctx context.Context, f applyConfigFlags, name string) error {
	if f.rollback || f.staged || f.diff || f.try != 0 || f.noConfirm || len(f.ignoreUnits) > 0 || f.force || f.payloads() || f.passwordFile != "" {
		return cli.Usagef("--confirm confirms the node's open trial and takes no other apply-config flag but the node's")
	}
	n := f.node
	n.needs = ""
	t, err := a.target(ctx, n, name)
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	conn.Close()
	if err != nil {
		return nodeError(err, t.name)
	}
	tr := resp.Msg.Trial
	if tr == nil {
		return fmt.Errorf("%s has no open trial to confirm; it runs config %s", t.name, cmp.Or(resp.Msg.ConfigVersion, "none"))
	}
	return a.confirmTrial(ctx, t, tr.Version, tr.PreviousVersion, tr.Deadline.AsTime())
}

// trialOpen refuses a change to a node whose status shows a config on trial, which the node
// refuses until the trial ends. A trial whose node runs the config from before it again, as a
// rollback at the deadline that failed after it wrote the config leaves it, holds nothing back:
// the node finishes that rollback before the change.
func trialOpen(name string, st *nodev1.StatusResponse) error {
	tr := st.GetTrial()
	if tr == nil || st.GetConfigVersion() == tr.PreviousVersion {
		return nil
	}
	return fmt.Errorf("%s runs config %s on trial until %s; confirm it with chalkctl apply-config --confirm %s, or roll it back with chalkctl apply-config --rollback %s, first",
		name, tr.Version, deadlineOf(tr), name, name)
}

// trialLine is the status line of a config on trial.
func trialLine(tr *nodev1.ConfigTrial) string {
	line := fmt.Sprintf("config %s on trial until %s: it rolls back to config %s unless apply-config --confirm confirms it", tr.Version, deadlineOf(tr), tr.PreviousVersion)
	if len(tr.IgnoreUnits) > 0 {
		line += "; its health check leaves out " + strings.Join(tr.IgnoreUnits, ", ")
	}
	switch {
	case tr.Error != "" && tr.RollbackChanged:
		line += "; the rollback at the deadline failed after it changed the node and is tried again each minute: " + tr.Error +
			"; apply-config --confirm no longer keeps the config, and apply-config --rollback finishes the rollback now"
	case tr.Error != "":
		line += "; the rollback at the deadline failed and is tried again each minute: " + tr.Error
	}
	return line
}

func deadlineOf(tr *nodev1.ConfigTrial) string {
	return tr.Deadline.AsTime().UTC().Format(time.RFC3339)
}

// failedUnitsWarning names the units the node lists as failed that the trial does not leave out,
// which keep its health check from passing unless they recover or the image ignores them; "" when
// there are none. Status lists every failed unit, also those the boot's check leaves out, so this
// warns rather than refuses.
func failedUnitsWarning(failed, ignore []string) string {
	var left []string
	for _, u := range failed {
		if !slices.Contains(ignore, u) {
			left = append(left, u)
		}
	}
	if len(left) == 0 {
		return ""
	}
	return "units failed already: " + strings.Join(left, ", ") + "; the trial rolls back at its deadline unless they recover, the image ignores them or --ignore-unit leaves them out"
}
