package chalkctl

import (
	"cmp"
	"context"
	"errors"
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
// it, and a trial not confirmed by then is waited for: the node rolls it back by itself. late
// tells that the deadline passed already, as for a trial whose rollback at the deadline failed,
// which the node still lets a confirmation end: its attempts end after trialEnd, and nothing
// rolls back when they fail. An answer that asking again does not change ends the attempts at
// once.
func (a *app) confirmTrial(ctx context.Context, t *target, version, previous string, deadline time.Time, late bool) error {
	until := deadline
	if late {
		until = a.now().Add(trialEnd)
	}
	fmt.Fprintf(a.stdout, "%s: waiting for a new connection and the node's health to confirm config %s\n", t.name, version)
	var last error
	permanent := false
	err := layer.WaitFor(ctx, "", until.Sub(a.now()), a.poll(), func() error {
		err := confirmOnce(ctx, t, version, until.Sub(a.now()))
		// An attempt the deadline cut short says less than the one before it.
		if err != nil && (last == nil || connect.CodeOf(err) != connect.CodeDeadlineExceeded) {
			last = err
		}
		switch connect.CodeOf(err) {
		// UNAVAILABLE is an unhealthy node, a trial that changed while the node checked its
		// health or a node not reached, which may all pass. INTERNAL is a trial record the node
		// cannot read, which only a rollback removes, or a failure asking again does not mend.
		case connect.CodeFailedPrecondition, connect.CodeInvalidArgument, connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInternal:
			permanent = true
			return layer.Permanent{Err: err}
		}
		return err
	})
	switch {
	case err == nil:
		fmt.Fprintf(a.stdout, "%s keeps config %s: the trial is confirmed\n", t.name, version)
		return nil
	// Interrupted, chalkctl leaves the trial open.
	case ctx.Err() != nil && late:
		return fmt.Errorf("%w; %s runs config %s on trial; %s", err, t.name, version, endTrialHint(t.name))
	case ctx.Err() != nil:
		return fmt.Errorf("%w; %s runs config %s on trial until %s; %s", err, t.name, version, deadline.UTC().Format(time.RFC3339), endTrialHint(t.name))
	}
	failure := fmt.Errorf("%s: config %s was not confirmed within its trial: %w", t.name, version, cmp.Or(last, err))
	switch {
	// A record the node cannot read names its remedy, which neither the deadline nor a
	// confirmation changes.
	case late, unreadableTrial(cmp.Or(last, err)):
		return failure
	case permanent:
		return fmt.Errorf("%w; it rolls back at %s unless confirmed; %s", failure, deadline.UTC().Format(time.RFC3339), endTrialHint(t.name))
	}
	return a.trialRolledBack(ctx, t, version, previous, deadline, failure)
}

// unreadableTrial reports whether err is the node's refusal of a trial whose record it cannot
// read.
func unreadableTrial(err error) bool {
	var ce *connect.Error
	return errors.As(err, &ce) && ce.Code() == connect.CodeInternal && strings.HasPrefix(ce.Message(), "read the record of the config's trial: ")
}

// confirmOnce asks the node to confirm the trial over a new connection, for at most timeout.
func confirmOnce(ctx context.Context, t *target, version string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ConfirmConfig(ctx, connect.NewRequest(&nodev1.ConfirmConfigRequest{Version: version}))
	return nodeError(err, t.name)
}

// trialRolledBack waits, over new connections, until trialEnd after the deadline, for the node to
// end the trial of config version that chalkctl could not confirm for failure, says how it ended,
// and returns failure. A node that kept the config, as when a confirmation went through but its
// answer came too late, ends it confirmed, without an error. A node that cannot read the record
// of the trial tells neither, and chalkctl names the rollback that removes it.
func (a *app) trialRolledBack(ctx context.Context, t *target, version, previous string, deadline time.Time, failure error) error {
	until := deadline.Add(trialEnd)
	var st *nodev1.StatusResponse
	unreadable := ""
	err := layer.WaitFor(ctx, "", until.Sub(a.now()), a.poll(), func() error {
		ctx, cancel := context.WithTimeout(ctx, until.Sub(a.now()))
		defer cancel()
		conn, err := dialInstalled(t)
		if err != nil {
			return err
		}
		defer conn.Close()
		resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			return err
		}
		// A record the node cannot read tells neither a confirmation nor a rollback, and holds
		// back every change until a rollback removes it.
		if e := resp.Msg.TrialError; e != "" {
			unreadable = e
			return nil
		}
		if tr := resp.Msg.Trial; tr != nil {
			if tr.Error != "" {
				return fmt.Errorf("its trial of config %s is still open: an attempt to roll the config back failed: %s", tr.Version, tr.Error)
			}
			return fmt.Errorf("its trial of config %s is still open", tr.Version)
		}
		st = resp.Msg
		return nil
	})
	switch {
	// Interrupted, chalkctl leaves the trial open.
	case ctx.Err() != nil:
		return fmt.Errorf("%w; %s may run config %s on trial until %s; %s", failure, t.name, version, deadline.UTC().Format(time.RFC3339), endTrialHint(t.name))
	case err != nil:
		return fmt.Errorf("%w; the node did not answer after the deadline either: %v; chalkctl status %s shows where it stands", failure, err, t.name)
	case unreadable != "":
		return fmt.Errorf("%w; the record of its trial cannot be read: %s; chalkctl apply-config --rollback %s restores the previous config", failure, unreadable, t.name)
	case st.ConfigVersion == version:
		fmt.Fprintf(a.stdout, "%s keeps config %s: the trial is confirmed\n", t.name, version)
		return nil
	case st.ConfigVersion == previous && a.now().Before(deadline):
		fmt.Fprintf(a.stdout, "%s runs config %s from before the trial\n", t.name, previous)
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
	if resp.Msg.TrialError != "" {
		return trialOpen(t.name, resp.Msg)
	}
	tr := resp.Msg.Trial
	if tr == nil {
		return fmt.Errorf("%s has no open trial to confirm; it runs config %s", t.name, cmp.Or(resp.Msg.ConfigVersion, "none"))
	}
	deadline := tr.Deadline.AsTime()
	return a.confirmTrial(ctx, t, tr.Version, tr.PreviousVersion, deadline, !a.now().Before(deadline))
}

// trialOpen refuses a change to a node whose status shows a config on trial, which the node
// refuses until the trial ends. A trial whose node runs the config from before it again, as a
// rollback at the deadline that failed after it wrote the config leaves it, holds nothing back:
// the node finishes that rollback before the change. A trial whose record the node cannot read
// holds back every change but a rollback.
func trialOpen(name string, st *nodev1.StatusResponse) error {
	if e := st.GetTrialError(); e != "" {
		return fmt.Errorf("%s: the record of a config's trial cannot be read: %s; other changes are refused until chalkctl apply-config --rollback %s restores the previous config", name, e, name)
	}
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
		line += "; an attempt to roll the config back failed after it changed the node and is tried again each minute: " + tr.Error +
			"; apply-config --confirm no longer keeps the config, and apply-config --rollback finishes the rollback now"
	case tr.Error != "":
		line += "; an attempt to roll the config back failed and is tried again each minute: " + tr.Error
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
