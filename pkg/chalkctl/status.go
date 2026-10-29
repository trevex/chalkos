package chalkctl

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/layers"
)

func (a *app) statusCommand() *cobra.Command {
	var n nodeFlags
	cmd := a.command(&cobra.Command{
		Use:     "status <node>",
		Short:   "Show an installed node's status",
		Long:    statusLong,
		Example: statusExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.status(ctx, n, pos) })
	n.registerClient(cmd.Flags())
	return cmd
}

func (a *app) status(ctx context.Context, n nodeFlags, pos []string) error {
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return err
	}
	s := resp.Msg
	state := t.cluster.configState(t.name, s.ConfigVersion, false)
	// A node installed before config documents runs an identity alone.
	fmt.Fprintf(a.stdout, "config %s (%s)\n", cmp.Or(s.ConfigVersion, "none"), state)
	if s.Boot.GetStaged() != nil {
		fmt.Fprintf(a.stdout, "staged config %s (%s)\n", cmp.Or(s.StagedConfigVersion, "none"), t.cluster.configState(t.name, s.StagedConfigVersion, true))
	}
	fmt.Fprintln(a.stdout, t.cluster.platformLine(t.name, s.Platform))
	for _, line := range bootLines(s.Boot) {
		fmt.Fprintln(a.stdout, line)
	}
	if line := t.cluster.imageLine(t.name, s.Boot); line != "" {
		fmt.Fprintln(a.stdout, line)
	}
	w := a.table()
	fmt.Fprintln(w, "VOLUME\tDISK\tMOUNT POINT\tSTATE")
	for _, v := range s.Volumes {
		st := "present"
		switch {
		case !v.Present:
			st = "missing"
		case v.Mounted:
			st = "mounted"
		case v.MountPoint != "":
			st = "not mounted"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.Name, v.Disk, v.MountPoint, st)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, d := range s.Disks {
		if d.Error != "" {
			fmt.Fprintf(a.stdout, "disk %s: %s\n", d.Name, d.Error)
		}
	}
	if len(s.Outputs) > 0 {
		w := a.table()
		fmt.Fprintln(w, "OUTPUT\tHASH\tSTATE")
		for _, o := range s.Outputs {
			fmt.Fprintln(w, outputLine(o))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		return err
	}
	if slices.Contains(info.Msg.Layers, layers.Kubernetes) {
		k, err := kubernetesStatus(ctx, conn)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, kubernetesLine(k))
		w := a.table()
		for _, line := range revokedLines(k) {
			fmt.Fprintln(w, line)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if slices.Contains(info.Msg.Layers, layers.Router) {
		resp, err := routerClient(conn).Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
		if err != nil {
			return err
		}
		if err := a.routerStatus(resp.Msg); err != nil {
			return err
		}
	}
	if len(s.Certificates) > 0 {
		fmt.Fprintln(a.stdout, "certificates:")
		w := a.table()
		for _, c := range s.Certificates {
			fmt.Fprintln(w, certificateLine(c, t.name))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if len(s.Trust) > 0 {
		fmt.Fprintln(a.stdout, "trust:")
		w := a.table()
		for _, tr := range s.Trust {
			fmt.Fprintln(w, trustLine(tr))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if s.Time != nil {
		fmt.Fprintln(a.stdout, timeLine(s.Time))
	}
	for _, u := range s.FailedUnits {
		fmt.Fprintf(a.stdout, "failed unit %s\n", u)
	}
	return nil
}

// routerStatus prints a router's BGP sessions, its NAT64 instances and its resolver, or why their
// state is unknown. A router without NAT64 instances or a resolver prints no line of them.
func (a *app) routerStatus(r *routerv1.StatusResponse) error {
	if err := a.bgpStatus(r); err != nil {
		return err
	}
	switch {
	case r.Nat64Error != "":
		fmt.Fprintf(a.stdout, "nat64: unknown: %s\n", r.Nat64Error)
	case len(r.Nat64) > 0:
		fmt.Fprintln(a.stdout, "nat64:")
		w := a.table()
		fmt.Fprintln(w, "  INSTANCE\tSESSIONS\tBINDINGS\tPOOL TCP\tPOOL UDP\tPOOL ICMP")
		for _, n := range r.Nat64 {
			pools := map[string]string{}
			for _, p := range n.Pools {
				if p.Size > 0 {
					pools[p.Protocol] = strconv.FormatUint(p.Size, 10)
				}
			}
			fmt.Fprintf(w, "  %s\t%d\t%d\t%s\t%s\t%s\n", n.Name, n.Sessions, n.Bindings, cmp.Or(pools["tcp"], "-"), cmp.Or(pools["udp"], "-"), cmp.Or(pools["icmp"], "-"))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	switch {
	case r.Resolver == nil:
	case r.Resolver.Running:
		fmt.Fprintln(a.stdout, "resolver: running")
	default:
		fmt.Fprintf(a.stdout, "resolver: not running: %s\n", r.Resolver.Error)
	}
	return nil
}

// bgpStatus prints a router's BGP sessions, or why their state is unknown.
func (a *app) bgpStatus(r *routerv1.StatusResponse) error {
	switch {
	case r.BgpError != "":
		fmt.Fprintf(a.stdout, "bgp: unknown: %s\n", r.BgpError)
		return nil
	case len(r.Peers) == 0:
		fmt.Fprintln(a.stdout, "bgp: no sessions")
		return nil
	}
	fmt.Fprintln(a.stdout, "bgp:")
	w := a.table()
	fmt.Fprintln(w, "  PEER\tNEIGHBOR\tSTATE\tUPTIME\tRECEIVED\tSENT\tBFD")
	for _, p := range r.Peers {
		uptime := "-"
		if p.State == "Established" {
			uptime = (time.Duration(p.UptimeSeconds) * time.Second).String()
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%d\t%d\t%s\n", p.Name, p.Neighbor, p.State, uptime, p.PrefixesReceived, p.PrefixesSent, cmp.Or(p.Bfd, "-"))
	}
	return w.Flush()
}

// bootLines are the status lines of the node's boot: the image it runs, an upgrade it boots next,
// and one it fell back from, with the lines that image logged last.
func bootLines(b *nodev1.BootStatus) []string {
	if b == nil {
		return nil
	}
	running := bootImageName(b.Running)
	if b.Error != "" {
		return []string{fmt.Sprintf("image %s; its boot is unknown: %s", running, b.Error)}
	}
	line := fmt.Sprintf("image %s, booted from %s", running, b.Entry)
	if !b.Blessed {
		line += ", not found healthy yet"
	}
	lines := []string{line}
	if b.Staged != nil {
		lines = append(lines, fmt.Sprintf("upgrade to %s installed; it boots next", bootImageName(b.Staged)))
	}
	if b.Failed != nil {
		lines = append(lines, fmt.Sprintf("upgrade to %s failed: rolled back to %s", bootImageName(b.Failed), running))
		for _, l := range b.Journal {
			lines = append(lines, "  "+l)
		}
	}
	return lines
}

// certificateLine is a certificate's line of the status, with tabs between its columns.
func certificateLine(c *nodev1.CertificateStatus, node string) string {
	line := fmt.Sprintf("  %s\texpires %s", c.Name, c.NotAfter.AsTime().UTC().Format(time.DateOnly))
	if c.NotAfter == nil {
		line = fmt.Sprintf("  %s\texpiry unknown", c.Name)
	}
	if c.Problem != "" {
		line += "\t" + strings.ReplaceAll(c.Problem, "<node>", node)
	}
	return line
}

// trustLine is a line of the status's trust section: what is trusted, by the first 16 hex digits
// of each fingerprint, the issuing one marked.
func trustLine(t *nodev1.TrustStatus) string {
	var fps []string
	for _, fp := range t.Fingerprints {
		short := fp[:min(16, len(fp))]
		if fp == t.Issuing {
			short += " (issues)"
		}
		fps = append(fps, short)
	}
	return fmt.Sprintf("  %s\t%s", t.Name, strings.Join(fps, ", "))
}

// timeLine is the status line of the node's clock.
func timeLine(t *nodev1.TimeStatus) string {
	switch {
	case t.Error != "":
		return "time: unknown: " + t.Error
	case !t.Synchronised:
		return "time: not synchronised; certificates are checked against this clock"
	}
	return fmt.Sprintf("time: synchronised to %s, offset %+.6f s", t.Source, t.OffsetSeconds)
}

// kubernetesLine is the status line of the node's Kubernetes side.
func kubernetesLine(k *kubernetesv1.StatusResponse) string {
	line := fmt.Sprintf("kubernetes %s: %s", k.Kind, k.State)
	if k.NodeReady != "" {
		line += ", node ready: " + k.NodeReady
	}
	if k.Vip != "" {
		line += ", vip " + k.Vip
	}
	if k.ControlPlane != "" {
		line += ", control plane " + k.ControlPlane
	}
	return line
}

// outputLine is an output's line of the status: the start of the hash of the files the node
// serves, and whether its last render passed, why it failed, or that none rendered it yet.
func outputLine(o *nodev1.OutputStatus) string {
	hash := "none"
	if len(o.Hash) >= 12 {
		hash = o.Hash[:12]
	}
	state := "rendered"
	switch {
	case o.Error != "":
		state = "failed: " + o.Error
	case o.Hash == "":
		state = "not rendered yet"
	}
	return o.Name + "\t" + hash + "\t" + state
}

// platformLine is the status line of the platform the node runs on, compared with its config's.
func (c *cluster) platformLine(name, reported string) string {
	if c.partial() {
		return "platform " + reported
	}
	want, err := c.platform(name)
	switch {
	case err != nil:
		return fmt.Sprintf("platform %s; the platform of its config's image is unknown: %v", reported, err)
	case reported != want:
		return fmt.Sprintf("platform %s, but %s; changing a node's platform is a reinstall", reported, c.declares(name, want))
	}
	return fmt.Sprintf("platform %s (the inventory's)", reported)
}

// configState says whether a slot of a node holds the config chalkctl delivers to it: the running
// image's, or with staged the staged image's.
func (c *cluster) configState(name, version string, staged bool) string {
	if c.partial() {
		return "no inventory to compare it with"
	}
	want, err := c.version(name)
	switch {
	case err != nil:
		return "cannot read the config the inventory gives it: " + err.Error()
	case want != version && staged:
		return "not the inventory's; chalkctl apply-config --staged " + name + " delivers it"
	case want != version:
		return "not the inventory's; chalkctl apply-config " + name + " delivers it"
	}
	return "the inventory's"
}

// imageLine is the status line of a node whose store has another root hash than the image the
// inventory names for it, or whose image directory cannot be read; empty when it has that image,
// or when the node or its config is unknown, which the config line reports.
func (c *cluster) imageLine(name string, boot *nodev1.BootStatus) string {
	running := boot.GetRunning().GetRootHash()
	if c.partial() || len(running) == 0 {
		return ""
	}
	nc, err := c.config(name)
	if err != nil {
		return ""
	}
	md, err := nc.Resolve()
	switch {
	case err != nil:
		return fmt.Sprintf("its store is not compared with its image in the inventory: %v", err)
	case hex.EncodeToString(running) == md.RootHash:
		return ""
	}
	line := fmt.Sprintf("its store's root hash is %x, not that of the image %s the inventory names, %s", running, nc.ImageName, md.RootHash)
	// After an upgrade with --no-reboot the image waits for the node's next boot.
	if want, err := hex.DecodeString(md.RootHash); err == nil && bytes.Equal(boot.GetStaged().GetRootHash(), want) {
		return fmt.Sprintf("%s; it has %s staged, which it boots with its next reboot: chalkctl reboot %s", line, bootImageName(boot.GetStaged()), name)
	}
	return fmt.Sprintf("%s; chalkctl upgrade --nodes %s installs that image", line, name)
}
