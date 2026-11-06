package chalkctl

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
)

// apiServerTag reads the API server's version from a config's text.
var apiServerTag = regexp.MustCompile(`kube-apiserver:v([0-9]+\.[0-9]+\.[0-9]+)`)

// apiServerVersion is the API server the node's config names; before any delivery its apiServer,
// 1.37.1 unless the test sets another.
func (n *upgradeFake) apiServerVersion() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if m := apiServerTag.FindStringSubmatch(n.config); m != nil {
		return m[1]
	}
	return cmp.Or(n.apiServer, "1.37.1")
}

// kubeletVersion is the kubelet of the node's image: its kubelet, 1.37.1 unless the test sets
// another.
func (n *upgradeFake) kubeletVersion() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return cmp.Or(n.kubelet, "1.37.1")
}

// controlPlanePods is "current" unless the lab's stuck holds the node, whose static pods never
// run the images its config names.
func (n *upgradeFake) controlPlanePods() string {
	n.lab.mu.Lock()
	defer n.lab.mu.Unlock()
	if n.lab.stuck[n.name] {
		return "kube-apiserver is not Ready"
	}
	return "current"
}

// refusedConfig is the node's refusal of its next delivery, as the lab's refuseConfig names it.
func (l *upgradeLab) refusedConfig(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refuseConfig == name {
		l.refuseConfig = ""
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the config is refused"))
	}
	return nil
}

func (n *upgradeFake) ConfirmConfig(ctx context.Context, req *connect.Request[nodev1.ConfirmConfigRequest]) (*connect.Response[nodev1.ConfirmConfigResponse], error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.trial == nil || n.trial.Version != req.Msg.Version {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no such trial"))
	}
	n.trial = nil
	n.lab.record("%s confirm", n.name)
	return connect.NewResponse(&nodev1.ConfirmConfigResponse{}), nil
}

func (n *upgradeFake) RollbackConfig(ctx context.Context, req *connect.Request[nodev1.RollbackConfigRequest]) (*connect.Response[nodev1.RollbackConfigResponse], error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.trial == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no trial"))
	}
	n.config, n.trial = n.trialPrevious, nil
	n.lab.record("%s rollback", n.name)
	return connect.NewResponse(&nodev1.RollbackConfigResponse{}), nil
}

// rollTo names the API server, controller-manager and scheduler of the version in the lab's
// cluster file.
func (l *upgradeLab) rollTo(t *testing.T, version string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(l.inventory), "cluster.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := componentTags.ReplaceAllString(string(data), "${1}:v"+version)
	writeFile(t, path, text)
}

// newRolloutLab is a lab of three control planes and w1 whose inventory names an image directory
// for each variant, which apply-config checks the config against. Its Kubernetes nodes run a
// config of the lab's Cluster document alone, which names the API server 1.37.1.
func newRolloutLab(t *testing.T) *upgradeLab {
	t.Helper()
	l := newUpgradeLabOf(t, 3, []string{"w1"}, "cp", "w")
	images := map[string]string{}
	for _, v := range []string{"cp", "w", "plain"} {
		images[v] = testUpgradeImage(t, v, "0.1.0")
	}
	l.useInventory(t, images)
	l.runAPIServer("1.37.1")
	return l
}

// componentTags are the tags of the components a Cluster document names that run on a control
// plane, digests included.
var componentTags = regexp.MustCompile(`(kube-(apiserver|controller-manager|scheduler)):v[0-9.]+(@sha256:[0-9a-f]+)?`)

// runAPIServer has the nodes named, or else every Kubernetes node of the lab, run a config of the
// lab's Cluster document alone, with the components of the control plane on the version.
func (l *upgradeLab) runAPIServer(version string, names ...string) {
	if len(names) == 0 {
		for name, n := range l.nodes {
			if n.kind != "" {
				names = append(names, name)
			}
		}
	}
	text := componentTags.ReplaceAllString(testClusterYAML, "${1}:v"+version)
	for _, name := range names {
		n := l.nodes[name]
		n.mu.Lock()
		n.config = text
		n.mu.Unlock()
	}
}

// applyConfigTo runs apply-config on the nodes with the lab's inventory and every node's address.
func (l *upgradeLab) applyConfigTo(names []string, args ...string) error {
	l.ta.stdout.Reset()
	l.ta.stderr.Reset()
	args = append(append([]string{"apply-config"}, names...), args...)
	return l.run(args...)
}

// deliveries are the events of deliveries, confirmations, rollbacks and etcd's quorum checks, the
// last as "members" whichever control plane was asked.
func deliveries(events []string) []string {
	var got []string
	for _, e := range events {
		switch {
		case strings.HasSuffix(e, " members"):
			got = append(got, "members")
		case strings.Contains(e, "apply-config"), strings.HasSuffix(e, " confirm"), strings.HasSuffix(e, " rollback"):
			got = append(got, e)
		}
	}
	return got
}

// A config that keeps the API server goes to the nodes in the order given, without trials or
// quorum checks; a run again leaves the nodes that run it alone.
func TestApplyConfigToSeveralNodes(t *testing.T) {
	l := newRolloutLab(t)
	if err := l.applyConfigTo([]string{"w1", "cp1"}); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := deliveries(l.takeEvents()), []string{"w1 apply-config staged=false", "cp1 apply-config staged=false"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q\n%s", got, want, l.ta.stdout)
	}
	if err := l.applyConfigTo([]string{"w1", "cp1"}); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := deliveries(l.takeEvents()); len(got) != 0 {
		t.Errorf("a run again: events %q", got)
	}
	for _, name := range []string{"w1", "cp1"} {
		if want := name + " runs config " + versionOf(l.inventoryConfig(name)) + " already\n"; !strings.Contains(l.ta.stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, l.ta.stdout)
		}
	}
}

// A config that keeps the API server asks no node's Kubernetes side anything, so it reaches nodes
// whose Kubernetes side does not answer, as a delivery that mends them does.
func TestApplyConfigWithoutAVersionChangeAsksNoLayer(t *testing.T) {
	l := newRolloutLab(t)
	l.kubernetesFails = map[string]bool{"cp1": true, "cp2": true, "w1": true}
	if err := l.applyConfigTo([]string{"cp1"}); err != nil {
		t.Fatalf("one node: %v\n%s", err, l.ta.stdout)
	}
	if err := l.applyConfigTo([]string{"w1", "cp2"}); err != nil {
		t.Fatalf("several nodes: %v\n%s", err, l.ta.stdout)
	}
	want := []string{"cp1 apply-config staged=false", "w1 apply-config staged=false", "cp2 apply-config staged=false"}
	if got := deliveries(l.takeEvents()); !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
}

// A config that changes the API server's version goes to the control planes first, one at a
// time, each after etcd's quorum check, on trial and confirmed once its control plane runs it;
// then the workers, without a trial.
func TestApplyConfigRollsOutTheAPIServer(t *testing.T) {
	l := newRolloutLab(t)
	l.rollTo(t, "1.37.2")
	if err := l.applyConfigTo([]string{"w1", "cp2", "cp1", "cp3"}); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	want := []string{
		"members", "cp1 apply-config staged=false", "cp1 confirm",
		"members", "cp2 apply-config staged=false", "cp2 confirm",
		"members", "cp3 apply-config staged=false", "cp3 confirm",
		"w1 apply-config staged=false",
	}
	if got := deliveries(l.takeEvents()); !slices.Equal(got, want) {
		t.Errorf("events %q, want %q\n%s", got, want, l.ta.stdout)
	}
	out := l.ta.stdout.String()
	if kept, next := strings.Index(out, "cp1 keeps config "), strings.Index(out, "cp2: delivering config "); kept < 0 || next < 0 || kept > next {
		t.Errorf("cp2 got the config before cp1 kept it:\n%s", out)
	}
	if !strings.Contains(out, "cp1: waiting for its layers to serve with the config\n") {
		t.Errorf("stdout = %s", out)
	}
	for name, n := range l.nodes {
		if name != "n1" && n.apiServerVersion() != "1.37.2" {
			t.Errorf("%s runs the API server %s", name, n.apiServerVersion())
		}
	}
}

// A control plane whose etcd would lose its quorum stops the run before anything is delivered;
// a cluster of one control plane needs --allow-downtime.
func TestApplyConfigRolloutKeepsQuorum(t *testing.T) {
	defer func(w time.Duration) { rolloutQuorumWait = w }(rolloutQuorumWait)
	rolloutQuorumWait = 100 * time.Millisecond
	l := newRolloutLab(t)
	l.rollTo(t, "1.37.2")
	l.unhealthyMembers = []string{"cp3"}
	err := l.applyConfigTo([]string{"cp1", "cp2", "cp3", "w1"})
	if err == nil || !strings.Contains(err.Error(), "without cp1 etcd has 1 healthy voter of 3, fewer than the 2 its quorum needs") {
		t.Errorf("err = %v", err)
	}
	if got := deliveries(l.takeEvents()); slices.ContainsFunc(got, func(e string) bool { return e != "members" }) {
		t.Errorf("events %q, want quorum checks alone", got)
	}

	single := newUpgradeLabOf(t, 1, []string{"w1"}, "cp", "w")
	single.useInventory(t, map[string]string{"cp": testUpgradeImage(t, "cp", "0.1.0"), "w": testUpgradeImage(t, "w", "0.1.0"), "plain": testUpgradeImage(t, "plain", "0.1.0")})
	single.runAPIServer("1.37.1")
	single.rollTo(t, "1.37.2")
	err = single.applyConfigTo([]string{"cp1"})
	if err == nil || !strings.Contains(err.Error(), "the API server of cp1 restarts, and with no other control plane the cluster's API may be unavailable meanwhile; pass --allow-downtime to accept that") {
		t.Errorf("one control plane: %v", err)
	}
	if err := single.applyConfigTo([]string{"cp1"}, "--allow-downtime"); err != nil {
		t.Fatalf("%v\n%s", err, single.ta.stdout)
	}
	if got := deliveries(single.takeEvents()); !slices.Contains(got, "cp1 confirm") {
		t.Errorf("events %q", got)
	}
}

// The first control plane whose components do not come up stops the run: its config is rolled
// back, the nodes after it get nothing, chalkctl prints who runs which API server, and the run
// again continues with it.
func TestApplyConfigRolloutStopsAndContinues(t *testing.T) {
	defer func(m time.Duration) { minTrial = m }(minTrial)
	minTrial = 0
	l := newRolloutLab(t)
	l.rollTo(t, "1.37.2")
	l.stuck = map[string]bool{"cp2": true}
	err := l.applyConfigTo([]string{"cp1", "cp2", "cp3", "w1"}, "--try", "200ms")
	if err == nil || !strings.Contains(err.Error(), "cp2: its control plane: kube-apiserver is not Ready") {
		t.Fatalf("err = %v\n%s", err, l.ta.stdout)
	}
	want := []string{"members", "cp1 apply-config staged=false", "cp1 confirm", "members", "cp2 apply-config staged=false", "cp2 rollback"}
	if got := deliveries(l.takeEvents()); !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	out := l.ta.stdout.String()
	for _, line := range []string{"API server v1.37.1: cp2, cp3, w1\n", "API server v1.37.2: cp1\n", "cp2 runs config "} {
		if !strings.Contains(out, line) {
			t.Errorf("stdout lacks %q:\n%s", line, out)
		}
	}
	l.stuck = nil
	if err := l.applyConfigTo([]string{"cp1", "cp2", "cp3", "w1"}); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if !strings.Contains(l.ta.stdout.String(), "cp1 runs config ") {
		t.Errorf("stdout = %q", l.ta.stdout)
	}
	if got := deliveries(l.takeEvents()); slices.ContainsFunc(got, func(e string) bool { return strings.HasPrefix(e, "cp1 ") }) {
		t.Errorf("the run again delivered to cp1 again: %q", got)
	}
}

// A node that refuses its config stops the run with its refusal; nothing reaches the nodes after
// it.
func TestApplyConfigRolloutStopsAtARefusal(t *testing.T) {
	l := newRolloutLab(t)
	l.rollTo(t, "1.37.2")
	l.refuseConfig = "cp2"
	err := l.applyConfigTo([]string{"cp1", "cp2", "cp3", "w1"})
	if err == nil || !strings.Contains(err.Error(), "the config is refused") {
		t.Fatalf("err = %v\n%s", err, l.ta.stdout)
	}
	for _, e := range deliveries(l.takeEvents()) {
		if strings.HasPrefix(e, "cp3 ") || strings.HasPrefix(e, "w1 ") {
			t.Errorf("the run went on: %s", e)
		}
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "API server v1.37.2: cp1\n") || !strings.Contains(out, "API server v1.37.1: cp2, cp3, w1\n") {
		t.Errorf("stdout = %s", out)
	}
}

// The version skew a delivery would break is refused before anything is delivered.
func TestApplyConfigRefusesSkew(t *testing.T) {
	controlPlanesOn := func(v string) func(l *upgradeLab) {
		return func(l *upgradeLab) { l.runAPIServer(v, "cp1", "cp2", "cp3") }
	}
	for name, tc := range map[string]struct {
		roll  string
		setup func(l *upgradeLab)
		nodes []string
		args  []string
		want  string
	}{
		"two minors newer":     {"1.39.0", nil, []string{"cp1", "cp2", "cp3", "w1"}, nil, "images.apiServer: v1.39.0 is 2 minors newer than the API server 1.37 that cp1 runs"},
		"a downgrade":          {"", controlPlanesOn("1.38.0"), []string{"cp1", "cp2", "cp3", "w1"}, nil, "images.apiServer: v1.37.1 is older than the API server 1.38 that cp1 runs; pass --allow-downgrade to downgrade the control plane"},
		"a downgrade, allowed": {"", controlPlanesOn("1.38.0"), []string{"cp1", "cp2", "cp3", "w1"}, []string{"--allow-downgrade"}, ""},
		"an old kubelet": {"1.38.0", func(l *upgradeLab) { l.nodes["w1"].kubelet = "1.34.0" }, []string{"cp1", "cp2", "cp3", "w1"}, nil,
			"images.apiServer: v1.38.0 is 4 minors newer than the kubelet 1.34 that w1 runs"},
		"a worker first": {"1.37.2", nil, []string{"w1"}, nil, "w1 is no control plane, and the control planes cp1, cp2, cp3 do not run the API server v1.37.2 yet; deliver the config to them first"},
		"a worker down": {"1.37.2", func(l *upgradeLab) { l.nodes["w1"].rebooting = true }, []string{"cp1", "cp2", "cp3"}, nil,
			"the versions w1 runs are unknown: "},
		"a node without Kubernetes down": {"1.37.2", func(l *upgradeLab) { l.nodes["n1"].rebooting = true }, []string{"cp1", "cp2", "cp3"}, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			l := newRolloutLab(t)
			if tc.roll != "" {
				l.rollTo(t, tc.roll)
			}
			if tc.setup != nil {
				tc.setup(l)
			}
			err := l.applyConfigTo(tc.nodes, tc.args...)
			events := deliveries(l.takeEvents())
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v\n%s", err, l.ta.stdout)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q\n%s", err, tc.want, l.ta.stdout)
			}
			if len(events) != 0 {
				t.Errorf("delivered: %q", events)
			}
		})
	}
}

// --until deliver leaves the first trial open, which apply-config --confirm confirms; the same
// command again goes on with the next node.
func TestApplyConfigUntilDeliver(t *testing.T) {
	l := newRolloutLab(t)
	l.rollTo(t, "1.37.2")
	all := []string{"cp1", "cp2", "cp3", "w1"}
	if err := l.applyConfigTo(all, "--until", "deliver"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := deliveries(l.takeEvents()), []string{"members", "cp1 apply-config staged=false"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if !strings.Contains(l.ta.stdout.String(), endTrialHint("cp1")) {
		t.Errorf("stdout = %s", l.ta.stdout)
	}
	if err := l.applyConfigTo(all, "--until", "deliver"); err == nil || !strings.Contains(err.Error(), "cp1 runs config ") || !strings.Contains(err.Error(), " on trial until ") {
		t.Errorf("a run during the trial: %v", err)
	}
	if err := l.applyConfigTo([]string{"cp1"}, "--confirm"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := deliveries(l.takeEvents()); !slices.Equal(got, []string{"cp1 confirm"}) {
		t.Errorf("events %q", got)
	}
	if err := l.applyConfigTo(all, "--until", "deliver"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := deliveries(l.takeEvents()), []string{"members", "cp2 apply-config staged=false"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if !strings.Contains(l.ta.stdout.String(), "cp1 runs config ") {
		t.Errorf("stdout = %s", l.ta.stdout)
	}
}

// --confirm waits for the node's layers before it confirms, and rolls the trial back at once when
// they do not find the node serving with the config.
func TestApplyConfigConfirmWaitsForTheLayers(t *testing.T) {
	defer func(m time.Duration) { minTrial = m }(minTrial)
	minTrial = 0
	l := newRolloutLab(t)
	l.stuck = map[string]bool{"cp1": true}
	if err := l.applyConfigTo([]string{"cp1"}, "--try", "2s", "--no-confirm"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	err := l.applyConfigTo([]string{"cp1"}, "--confirm")
	if err == nil || !strings.Contains(err.Error(), "cp1: its control plane: kube-apiserver is not Ready") {
		t.Fatalf("err = %v\n%s", err, l.ta.stdout)
	}
	if got, want := deliveries(l.takeEvents()), []string{"cp1 apply-config staged=false", "cp1 rollback"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
}

// A trial whose node does not answer for a while after the delivery, as a config that restarts
// its network leaves it, is confirmed once the node answers again within the trial.
func TestApplyConfigTrialWaitsForTheNodeToAnswer(t *testing.T) {
	defer func(m time.Duration) { minTrial = m }(minTrial)
	minTrial = 0
	l := newRolloutLab(t)
	l.dropAfterDelivery = map[string]int{"w1": 3}
	if err := l.applyConfigTo([]string{"w1"}, "--try", "5s"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := deliveries(l.takeEvents()), []string{"w1 apply-config staged=false", "w1 confirm"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q\n%s", got, want, l.ta.stdout)
	}
}

// A layer whose check does not answer holds a trial no longer than its deadline: the trial is
// rolled back then.
func TestApplyConfigTrialEndsAtALayerThatDoesNotAnswer(t *testing.T) {
	defer func(m time.Duration) { minTrial = m }(minTrial)
	minTrial = 0
	l := newRolloutLab(t)
	l.hangAfterDelivery = "w1"
	done := make(chan error, 1)
	go func() { done <- l.applyConfigTo([]string{"w1"}, "--try", "1s") }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "was not confirmed within its trial") {
			t.Errorf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("apply-config still waits for the layer after the trial's deadline")
	}
	if got, want := deliveries(l.takeEvents()), []string{"w1 apply-config staged=false", "w1 rollback"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
}

// A delivery that changes the API server goes without the layers' payloads.
func TestApplyConfigRolloutRefusesPayloads(t *testing.T) {
	l := newRolloutLab(t)
	l.rollTo(t, "1.37.2")
	err := l.applyConfigTo([]string{"cp1"}, "--kubernetes-share")
	if err == nil || !strings.Contains(err.Error(), "the config changes versions the cluster orders its nodes by, so the nodes that act for the others get it on trial, which --kubernetes-share does not go with; deliver it apart") {
		t.Errorf("err = %v", err)
	}
	if got := deliveries(l.takeEvents()); len(got) != 0 {
		t.Errorf("events %q", got)
	}
}

// --address gives one node's address bare, several nodes' as NODE=ADDR.
func TestApplyConfigAddresses(t *testing.T) {
	l := newRolloutLab(t)
	run := func(args ...string) error {
		l.ta.stdout.Reset()
		return l.ta.run(context.Background(), append(append([]string{"apply-config"}, args...), "--inventory", l.inventory))
	}
	if err := run("cp1", "w1", "--address", l.addrs["cp1"]); err == nil || !strings.Contains(err.Error(), "with several nodes, give each node's address as NODE=ADDR") {
		t.Errorf("a bare address: %v", err)
	}
	if err := run("cp1", "w1", "--address", "cp1="+l.addrs["cp1"], "--address", "w1="+l.addrs["w1"]); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := deliveries(l.takeEvents()), []string{"cp1 apply-config staged=false", "w1 apply-config staged=false"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if err := run("w1", "--address", l.addrs["w1"]); err != nil {
		t.Fatalf("one node, a bare address: %v\n%s", err, l.ta.stdout)
	}
}

// testImageWithKubelet is a test image of the variant whose image.json names a kubelet of the
// version.
func testImageWithKubelet(t *testing.T, variant, name, version string) string {
	t.Helper()
	dir := testUpgradeImage(t, variant, name)
	k := `{"version": "` + version + `", "cni": "flannel"}`
	writeFile(t, filepath.Join(dir, "image.json"), k)
	md, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "metadata.json"), strings.Replace(string(md), `{"version": "1.37.1", "cni": "flannel"}`, k, 1))
	return dir
}

// An image whose kubelet would be newer than the oldest API server the control planes run is
// refused before any node is sent anything; patch releases pass, and a rollback asks nothing.
func TestUpgradeRefusesAKubeletNewerThanTheAPIServer(t *testing.T) {
	l := newUpgradeLab(t, 3)
	newer := testImageWithKubelet(t, "w", "0.2.0", "1.38.0")
	upgraded := func() bool {
		return slices.ContainsFunc(l.takeEvents(), func(e string) bool { return strings.HasSuffix(e, " upgrade") })
	}
	err := l.upgrade(newer, "--nodes", "w1", "--until", "activate")
	if want := "w1: the image's kubelet 1.38.0 would be newer than the API server 1.37.1 that cp1 runs, the oldest of the cluster; upgrade the control plane first: name the new release's images in the Cluster document and deliver it with chalkctl apply-config"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	if upgraded() {
		t.Error("a refused image was sent")
	}

	l.nodes["cp2"].rebooting = true
	if err := l.upgrade(testImageWithKubelet(t, "w", "0.3.0", "1.37.5"), "--nodes", "w1", "--until", "activate"); err == nil || !strings.Contains(err.Error(), "the versions cp2 runs are unknown: ") || !strings.Contains(err.Error(), "the version skew check needs every control plane") {
		t.Errorf("cp2 down: %v", err)
	}
	if upgraded() {
		t.Error("an image was sent while cp2 was down")
	}
	l.nodes["cp2"].rebooting = false

	if err := l.upgrade(testImageWithKubelet(t, "w", "0.3.0", "1.37.5"), "--nodes", "w1", "--until", "activate"); err != nil {
		t.Errorf("a patch release: %v\n%s", err, l.ta.stdout)
	}
	l.nodes["w2"].rebooting = true
	if err := l.upgrade(testUpgradeImage(t, "cp", "0.2.0"), "--nodes", "cp1", "--until", "activate"); err != nil {
		t.Errorf("w2 down: %v\n%s", err, l.ta.stdout)
	}
	l.nodes["w2"].rebooting = false
	for _, name := range []string{"cp1", "cp2", "cp3"} {
		l.nodes[name].apiServer = "1.38.0"
	}
	l.rollTo(t, "1.38.0")
	if err := l.upgrade(newer, "--nodes", "w3"); err != nil {
		t.Fatalf("API servers on 1.38: %v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	// A rollback opens no image, so it checks none, and a control plane that is down does not
	// stop it.
	l.nodes["cp2"].rebooting = true
	if err := l.run("upgrade", "--rollback", "--nodes", "w3"); err != nil {
		t.Errorf("a rollback: %v\n%s", err, l.ta.stdout)
	}
}

// An upgrade whose config would change the API server a node runs is refused before any node is
// sent anything; once apply-config delivered the config, the upgrade goes ahead.
func TestUpgradeRefusesAConfigThatChangesTheAPIServer(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.runAPIServer("1.37.1")
	l.rollTo(t, "1.37.2")
	img := testUpgradeImage(t, "w", "0.2.0")
	err := l.upgrade(img, "--nodes", "w1", "--until", "activate")
	if want := "w1: the inventory's config changes the API server from 1.37.1 to 1.37.2, which an upgrade does not deliver; deliver the config with chalkctl apply-config first, which orders the nodes for it, then upgrade"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("events %q, want none", events)
	}
	l.runAPIServer("1.37.2")
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.nodes["w1"].staged; got != "0.2.0" {
		t.Errorf("w1 stages %q", got)
	}
}

// An upgrade of a node outside the Kubernetes cluster reaches none of the cluster's nodes, so a
// control plane that does not answer does not stop it; nor does it stop apply-config to that node,
// whose config names the cluster's API server too.
func TestOutsideTheClusterReachesNoneOfIt(t *testing.T) {
	l := newRolloutLab(t)
	l.nodes["cp1"].rebooting = true
	if err := l.upgrade(testUpgradeImage(t, "plain", "0.2.0"), "--nodes", "n1", "--until", "activate"); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.runAPIServer("1.37.1", "n1")
	l.rollTo(t, "1.37.2")
	if err := l.applyConfigTo([]string{"n1"}); err != nil {
		t.Fatalf("apply-config: %v\n%s", err, l.ta.stdout)
	}
	if got, want := deliveries(l.takeEvents()), []string{"n1 apply-config staged=false"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
}

// otherLayer is a layer whose cluster a node may coordinate besides the one an ordered run
// orders.
type otherLayer struct{ layer.Upgrade }

func (otherLayer) Name() string { return "other" }

// A node goes first in an ordered run when it acts for the others in the cluster of a layer that
// orders the run, not in another layer's.
func TestRolloutCoordinatorOfTheOrderedLayer(t *testing.T) {
	k := compiled[layer.Rollout]()[0]
	r := &configRollout{ordered: []layer.Rollout{k}, nodes: map[string]*rolloutNode{}}
	ordering := k.(layer.Upgrade)
	for name, roles := range map[string]upgradeRoles{
		"a":  {{otherLayer{}, layer.UpgradeRole{Coordinator: true}}, {ordering, layer.UpgradeRole{Member: true}}},
		"b":  {{ordering, layer.UpgradeRole{Coordinator: true}}},
		"c0": {{otherLayer{}, layer.UpgradeRole{Coordinator: true}}},
	} {
		n := &rolloutNode{}
		n.name, n.roles = name, roles
		r.nodes[name] = n
	}
	if got, want := r.order(), []string{"b", "a", "c0"}; !slices.Equal(got, want) {
		t.Errorf("order %q, want %q", got, want)
	}
}
