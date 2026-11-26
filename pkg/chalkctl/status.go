package chalkctl

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/statusview"
)

type statusFlags struct {
	node   nodeFlags
	output layer.Output
}

func (a *app) statusCommand() *cobra.Command {
	var f statusFlags
	cmd := a.command(&cobra.Command{
		Use:         "status [<node>]",
		Short:       "Show an installed node's status, or a line for each node of the cluster",
		Long:        statusLong,
		Example:     statusExample,
		Args:        cli.RangeArgs(0, 1),
		Annotations: layer.Meta{Credentials: layer.Reader}.Annotations(),
	}, func(a *app, ctx context.Context, pos []string) error { return a.status(ctx, f, pos) })
	f.node.RegisterClient(cmd.Flags())
	f.output.Register(cmd.Flags())
	return cmd
}

func (a *app) status(ctx context.Context, f statusFlags, pos []string) error {
	if len(pos) == 0 {
		return a.clusterStatus(ctx, f)
	}
	t, err := a.clientTarget(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	var r *statusRead
	if err := callNode(t, func(conn *client.Conn) error {
		r, err = a.readStatus(ctx, t, conn)
		return err
	}); err != nil {
		return err
	}
	if f.output.JSON() {
		s, err := r.json()
		if err != nil {
			return err
		}
		return layer.WriteJSON(a.stdout, s)
	}
	fmt.Fprintln(a.stdout, statusview.SummaryLine(t.name, r.problems))
	return statusview.Write(a.stdout, r.status, r.options)
}

// statusRead is what status read of a node: its Status and Info, the sections of its layers, the
// options statusview writes the status with and the problems the status shows.
type statusRead struct {
	name     string
	status   *nodev1.StatusResponse
	info     *nodev1.InfoResponse
	layers   []layerSection
	options  statusview.Options
	problems []string
	// compared is set when an inventory compares the node.
	compared bool
}

// layerSection is a layer's section of a node's status, by the layer's name.
type layerSection struct {
	name string
	layer.StatusSection
}

// readStatus reads the status of the node t over conn, with the sections of the layers its Info
// names.
func (a *app) readStatus(ctx context.Context, t *target, conn *client.Conn) (*statusRead, error) {
	st, err := conn.Status(ctx, &nodev1.StatusRequest{})
	if err != nil {
		return nil, err
	}
	info, err := conn.Info(ctx, &nodev1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	r := &statusRead{name: t.name, status: st, info: info, compared: !t.cluster.Partial()}
	for _, p := range plugins {
		s, ok := p.(layer.Status)
		if !ok || !slices.Contains(info.Layers, p.Name()) {
			continue
		}
		section, err := s.ReadStatus(ctx, conn)
		if err != nil {
			// A layer's section the node could not answer shows in its place, as chalkd's own do.
			section = unreadSection(p.Name(), client.Describe(err, t.hints()))
		}
		r.layers = append(r.layers, layerSection{p.Name(), section})
	}
	r.options = statusview.Options{
		Node:         t.name,
		Inventory:    nodeInventory{c: t.cluster, name: t.name},
		ModulePolicy: info.ModulePolicy,
		Layers: func(w io.Writer) error {
			for _, l := range r.layers {
				if err := l.Write(w); err != nil {
					return err
				}
			}
			return nil
		},
	}
	r.problems = statusview.Problems(st, r.options)
	for _, l := range r.layers {
		r.problems = append(r.problems, l.Problems...)
	}
	return r, nil
}

// unreadSection is the section of the layer called name, whose status could not be read for err.
func unreadSection(name string, err error) layer.StatusSection {
	return layer.StatusSection{
		Write: func(w io.Writer) error {
			_, werr := fmt.Fprintln(w, statusview.SectionErrorLine(name, err.Error()))
			return werr
		},
		Problems: []string{name + " unknown"},
	}
}

// statusJSON is a node's status as status -o json prints it.
type statusJSON struct {
	// Node is the node's name in the inventory.
	Node string `json:"node"`
	// Error says why the node could not be read, as status without a node prints it; the fields
	// after Problems are then left out.
	Error string `json:"error,omitempty"`
	// Healthy is set when the node was read and shows no problem.
	Healthy bool `json:"healthy"`
	// Problems are what the node's status shows wrong, each in a few words, as the summary line
	// names them; their wording may change, their number does not without a cause.
	Problems []string `json:"problems"`
	// Inventory compares the node with the inventory; left out without one.
	Inventory *statusview.Comparison `json:"inventory,omitempty"`
	// Info and Status are the node's answers to the node API's Info and Status, in protojson.
	Info   json.RawMessage `json:"info,omitempty"`
	Status json.RawMessage `json:"status,omitempty"`
	// Layers are the layers' statuses by the layer's name, each in protojson.
	Layers map[string]json.RawMessage `json:"layers,omitempty"`
}

// json is the node's status as status -o json prints it.
func (r *statusRead) json() (statusJSON, error) {
	s := statusJSON{Node: r.name, Healthy: len(r.problems) == 0, Problems: r.problems}
	if s.Problems == nil {
		s.Problems = []string{}
	}
	if r.compared {
		c := r.options.Inventory.Compare(r.status)
		s.Inventory = &c
	}
	var err error
	if s.Info, err = layer.ProtoJSON(r.info); err != nil {
		return statusJSON{}, err
	}
	if s.Status, err = layer.ProtoJSON(r.status); err != nil {
		return statusJSON{}, err
	}
	for _, l := range r.layers {
		if l.Message == nil {
			continue
		}
		if s.Layers == nil {
			s.Layers = map[string]json.RawMessage{}
		}
		if s.Layers[l.name], err = layer.ProtoJSON(l.Message); err != nil {
			return statusJSON{}, err
		}
	}
	return s, nil
}

// clusterStatusTimeout bounds the read of each node's status by status without a node, so a node
// that does not answer is reported as unreachable while the others are.
const clusterStatusTimeout = 15 * time.Second

// clusterStatus reads the status of every node of the inventory at once and prints a line of
// each, or with -o json each node's status. A node that cannot be read is unreachable and fails
// none of the others.
func (a *app) clusterStatus(ctx context.Context, f statusFlags) error {
	s, err := a.reach(ctx, f.node, "", true)
	if err != nil {
		return err
	}
	if err := s.cluster.RequireNodes("status without a node"); err != nil {
		return err
	}
	names := s.cluster.NodeNames()
	reads := make([]*statusRead, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, clusterStatusTimeout)
			defer cancel()
			t, err := s.target(name)
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = callNode(t, func(conn *client.Conn) error {
				reads[i], err = a.readStatus(ctx, t, conn)
				return err
			})
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.output.JSON() {
		out := struct {
			Nodes []statusJSON `json:"nodes"`
		}{Nodes: make([]statusJSON, len(names))}
		for i, name := range names {
			if errs[i] != nil {
				out.Nodes[i] = statusJSON{Node: name, Error: layer.Unnamed(name, errs[i]).Error(), Problems: []string{}}
				continue
			}
			if out.Nodes[i], err = reads[i].json(); err != nil {
				return err
			}
		}
		return layer.WriteJSON(a.stdout, out)
	}
	w := a.table()
	fmt.Fprintln(w, "NODE\tIMAGE\tBOOT\tCONFIG\tPROBLEMS")
	for i, name := range names {
		if errs[i] != nil {
			fmt.Fprintf(w, "%s\t-\tunreachable\t-\t-\n", name)
			continue
		}
		r := reads[i]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", name, statusview.BootImageName(r.status.GetBoot().GetRunning()), bootState(r.status.GetBoot()), r.configState(), len(r.problems))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for i, err := range errs {
		if err != nil {
			fmt.Fprintf(a.stderr, "chalkctl: %s is unreachable: %v\n", names[i], layer.Unnamed(names[i], err))
		}
	}
	return nil
}

// bootState is the BOOT column of a node whose boot status is b.
func bootState(b *nodev1.BootStatus) string {
	switch {
	case b.GetError() != "":
		return "unknown"
	case b.GetBlessed():
		return "healthy"
	}
	return "not healthy"
}

// configState is the CONFIG column of the node: a trial and its deadline, else how its config
// compares with the inventory's.
func (r *statusRead) configState() string {
	if tr := r.status.GetTrial(); tr != nil {
		return "on trial until " + statusview.DeadlineOf(tr)
	}
	if !r.compared {
		return statusview.Unknown
	}
	switch c := r.options.Inventory.Compare(r.status).Config; c {
	case statusview.Same:
		return "inventory's"
	default:
		return c
	}
}

// nodeInventory compares the node name with the inventory of c, as statusview asks.
type nodeInventory struct {
	c    *cluster
	name string
}

// PlatformLine is the status line of the platform the node runs on, compared with its config's.
func (i nodeInventory) PlatformLine(reported string) string {
	c, name := i.c, i.name
	if c.Partial() {
		return "platform " + reported
	}
	nc, md, err := c.image(name)
	switch {
	case err != nil:
		return fmt.Sprintf("platform %s; the platform of its config's image is unknown: %v", reported, err)
	case reported != md.Platform:
		return fmt.Sprintf("platform %s, but its image %s is built for %s; changing a node's platform is a reinstall", reported, nc.ImageName, md.Platform)
	}
	return fmt.Sprintf("platform %s (the inventory's)", reported)
}

// ConfigState says whether the running image's slot of the node the status s describes holds the
// config chalkctl delivers to it, as runningAdvice advises.
func (i nodeInventory) ConfigState(s *nodev1.StatusResponse) string {
	name, img := i.name, i.imageOf(s.Boot)
	return i.slotState(s.ConfigVersion, "chalkctl config apply --diff "+name, func(want string) string { return runningAdvice(name, want, img, s) })
}

// StagedConfigState says whether the slot of the image the status s names as staged holds the
// config chalkctl delivers to the node, as stagedAdvice advises.
func (i nodeInventory) StagedConfigState(s *nodev1.StatusResponse) string {
	name, img := i.name, i.imageOf(s.Boot)
	return i.slotState(s.StagedConfigVersion, "chalkctl config apply --staged --diff "+name, func(want string) string { return stagedAdvice(name, want, img, s) })
}

// slotState says whether a slot of the node holds version, the config chalkctl delivers to it, and
// when it does not, what advice says delivers it, given the inventory's version. diff is the command
// that compares the slot's config with the inventory's when status cannot.
func (i nodeInventory) slotState(version, diff string, advice func(want string) string) string {
	c, name := i.c, i.name
	if c.Partial() {
		return "no inventory to compare it with"
	}
	want, err := c.version(name)
	switch {
	case errors.Is(err, errEncryptedSources):
		// status decrypts nothing, so it does not know the config's version.
		return "not compared with the inventory's, which takes Secret values from sops or age sources that status does not decrypt; " + diff + " compares them"
	case err != nil:
		return "cannot read the config the inventory gives it: " + err.Error()
	case want != version:
		return "not the inventory's; " + advice(want)
	}
	return "the inventory's"
}

// trialAdvice says what holds back every delivery to the node of the status s, as trialHold
// tells; "" when nothing does. want is the inventory's config, whose trial's confirmation writes
// the staged slot.
func trialAdvice(name, want string, s *nodev1.StatusResponse) string {
	tr, unreadable := trialHold(s)
	switch {
	case unreadable:
		return "the node takes no delivery until chalkctl config rollback " + name + " removes the record of a config's trial that cannot be read"
	case tr == nil:
		return ""
	case tr.Version == want:
		return "the confirmation of its trial writes it: chalkctl config confirm " + name
	}
	return "the node takes no delivery until the trial of config " + tr.Version + " ends"
}

// runningAdvice says what delivers want, the inventory's config, to the running image's slot.
// config apply checks the config against the image the node runs, which need not accept the
// config of another image, so a node that runs another image than the inventory's gets the config
// with the inventory's image. A known-good image boots with the config it last ran, so the config
// follows its boot.
func runningAdvice(name, want string, img imageMatch, s *nodev1.StatusResponse) string {
	if a := trialAdvice(name, want, s); a != "" {
		return a
	}
	switch {
	case img.runs:
		return "chalkctl config apply " + name + " delivers it" + img.otherChecks()
	case img.stages:
		return "it comes with the image " + img.name + " in the inactive slot"
	case img.knownGood:
		return "the node boots the image " + img.name + " next with the config it last ran; once it runs it, chalkctl config apply " + name + " delivers it"
	}
	return "chalkctl upgrade " + name + " delivers it with the image " + img.name
}

// stagedAdvice says what delivers want, the inventory's config, to the staged image's slot. A plain
// config apply writes both slots, except that of a rollback's known-good image, which keeps its
// config until the boot is found healthy, as the status names it. A trial holds back every
// delivery but its confirmation. config apply --staged writes the staged slot alone, checked
// against the inventory's image; it is the advice when the running image runs want already, or
// when the staged image is the inventory's and the running one is not and may refuse want. With
// neither image the inventory's, the upgrade delivers want.
func stagedAdvice(name, want string, img imageMatch, s *nodev1.StatusResponse) string {
	b := s.Boot
	if b.GetStagedKnownGood() {
		return "the rollback image keeps the config it last ran while the boot of " + b.GetEntry() + " is not found healthy"
	}
	if a := trialAdvice(name, want, s); a != "" {
		return a
	}
	switch {
	case !img.runs && !img.stages:
		return "chalkctl upgrade " + name + " delivers it with the image " + img.name
	case s.ConfigVersion == want || !img.runs:
		return "chalkctl config apply --staged " + name + " delivers it" + img.otherChecks()
	}
	return "chalkctl config apply " + name + " delivers it" + img.otherChecks()
}

// imageMatch is what a node's boot status says of the image the inventory names for it.
type imageMatch struct {
	// name and rootHash are the image's name in the inventory and its root hash.
	name, rootHash string
	// runs says whether the node runs the image, and is true when status cannot compare the
	// images.
	runs bool
	// stages says whether the inactive slot holds the image to boot with the config deliveries
	// write there; knownGood whether it holds it as the known-good image, which keeps the config
	// it last ran.
	stages, knownGood bool
	// other names another image in the inactive slot that checks each delivery to the running
	// image the inventory names, as it takes the config too; "" without one.
	other string
	// err says why the inventory's image directory cannot be read.
	err error
}

// otherChecks is what the advice of a config delivery adds for another image in the inactive
// slot, which no command of chalkctl removes and which may refuse the inventory's config.
func (m imageMatch) otherChecks() string {
	if m.other == "" {
		return ""
	}
	return " if the image " + m.other + " in the inactive slot, which is not the inventory's, accepts it too"
}

// imageOf compares the images boot reports with the image the inventory names for the node.
func (i nodeInventory) imageOf(boot *nodev1.BootStatus) imageMatch {
	running := boot.GetRunning().GetRootHash()
	if i.c.Partial() || len(running) == 0 {
		return imageMatch{runs: true}
	}
	nc, md, err := i.c.image(i.name)
	switch {
	case nc == nil:
		return imageMatch{runs: true}
	case err != nil:
		return imageMatch{name: nc.ImageName, runs: true, err: err}
	}
	m := imageMatch{name: nc.ImageName, rootHash: md.RootHash, runs: hex.EncodeToString(running) == md.RootHash}
	staged := boot.GetStaged()
	switch {
	case staged == nil:
	case hex.EncodeToString(staged.GetRootHash()) != md.RootHash:
		if m.runs && !boot.GetStagedKnownGood() {
			m.other = statusview.BootImageName(staged)
		}
	case boot.GetStagedKnownGood():
		m.knownGood = true
	default:
		m.stages = true
	}
	return m
}

// ImageLine is the status line of a node whose store has another root hash than the image the
// inventory names for it, or whose image directory cannot be read; empty when it has that image,
// or when the node or its config is unknown, which the config line reports.
func (i nodeInventory) ImageLine(boot *nodev1.BootStatus) string {
	img := i.imageOf(boot)
	switch {
	case img.err != nil:
		return fmt.Sprintf("its store is not compared with its image in the inventory: %v", img.err)
	case img.runs:
		return ""
	}
	line := fmt.Sprintf("its store's root hash is %x, not that of the image %s the inventory names, %s", boot.GetRunning().GetRootHash(), img.name, img.rootHash)
	staged := statusview.BootImageName(boot.GetStaged())
	// After an upgrade --until stage the image waits for its activation, after --until activate for
	// the node's next boot. A rollback from a boot not found healthy leaves the known-good image to
	// boot next.
	switch {
	case img.stages && boot.GetStagedState() == nodev1.SlotState_SLOT_STATE_STAGED:
		return fmt.Sprintf("%s; it has %s staged: chalkctl node activate %s makes it boot next", line, staged, i.name)
	case img.stages:
		return fmt.Sprintf("%s; it has %s active, which it boots with its next reboot: chalkctl node reboot %s", line, staged, i.name)
	case img.knownGood:
		return fmt.Sprintf("%s; it has %s active as its known-good image, which it boots with the config it last ran at its next reboot: chalkctl node reboot %s", line, staged, i.name)
	}
	return fmt.Sprintf("%s; chalkctl upgrade %s installs that image", line, i.name)
}

// Compare compares the node the status s describes with the inventory, as status -o json prints
// it.
func (i nodeInventory) Compare(s *nodev1.StatusResponse) statusview.Comparison {
	c := statusview.Comparison{Config: i.configMatch(s.ConfigVersion), Image: statusview.Unknown, Platform: statusview.Unknown}
	if s.GetBoot().GetStaged() != nil {
		c.StagedConfig = i.configMatch(s.StagedConfigVersion)
	}
	if i.c.Partial() {
		return c
	}
	_, md, err := i.c.image(i.name)
	if err != nil {
		return c
	}
	c.Platform = match(s.Platform == md.Platform)
	if running := s.GetBoot().GetRunning().GetRootHash(); len(running) > 0 {
		c.Image = match(hex.EncodeToString(running) == md.RootHash)
	}
	return c
}

// configMatch compares version, the config of a slot of the node, with the inventory's.
func (i nodeInventory) configMatch(version string) string {
	if i.c.Partial() {
		return statusview.Unknown
	}
	want, err := i.c.version(i.name)
	if err != nil {
		return statusview.Unknown
	}
	return match(version == want)
}

// match is same when same is set, else differs.
func match(same bool) string {
	if same {
		return statusview.Same
	}
	return statusview.Differs
}
