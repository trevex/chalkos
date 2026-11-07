package chalkctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/storage"
)

// apply-config --dry-run prints the diff and the node's plan of the delivery, and the node
// changes nothing: it restarts no unit and keeps its config.
func TestApplyConfigDryRun(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "--dry-run", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "n1: config 3 added, 0 removed, 0 changed\n" +
		"+ Cluster \"default\"\n+ Network \"10-uplink\"\n+ SystemDisk \"system\"\n" +
		"n1: config none → " + ta.configVersion(t) + "\n" +
		"  outputs:  none\n" +
		"  effects:  containerd-config: restart chalkos-containerd-config.service containerd.service (rule)\n" +
		"            kubernetes: restart chalkos-kubernetes.service kubelet.service (rule)\n" +
		"  storage:  none\n"
	if ta.stdout.String() != want {
		t.Errorf("stdout = %q, want %q", ta.stdout, want)
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "systemctl restart") || strings.HasPrefix(c, "systemctl show") {
			t.Errorf("node ran %s", c)
		}
	}
	if v, err := slot.Version(slotOf(t, s)); err != nil || v != "" {
		t.Errorf("config version on the node = %q, %v; want none", v, err)
	}
}

// --dry-run checks the nodes named one after the other: a node it cannot check does not stop the
// others, and the command fails naming it apart from the nodes whose config is refused.
func TestApplyConfigDryRunOfSeveralNodes(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	writeFile(t, ta.inventory, strings.Replace(testInventoryYAML, "10.0.0.11", addr, 1))
	err := ta.run(context.Background(), []string{"apply-config", "--dry-run", "n9", "n1", "--inventory", ta.inventory})
	if err == nil || err.Error() != "the dry run failed on 1 node: n9" {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(ta.stderr.String(), "chalkctl: n9: ") || !strings.Contains(ta.stdout.String(), "n1: config none → "+ta.configVersion(t)) {
		t.Errorf("stdout = %q, stderr = %q", ta.stdout, ta.stderr)
	}
}

// A config the node refuses on a dry run is refused, and a dry run of it alone exits with the
// node's refusal; one of several nodes is counted apart from a node the dry run failed on. A
// config chalkctl refuses itself, against the image's schemas, is counted as refused too.
func TestApplyConfigDryRunRefused(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{}`))
	// The node recorded VAR as xfs, so the inventory's ext4 takes a reinstall, which the node
	// refuses.
	section := ta.identity(t).Storage
	v := section.Volumes["var"]
	v.Format = "xfs"
	section.Volumes = map[string]storage.Volume{"var": v}
	data, _ := json.Marshal(section)
	writeFile(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), string(data))
	addr := ta.startNode(t, s)
	writeFile(t, ta.inventory, strings.Replace(testInventoryYAML, "10.0.0.11", addr, 1))

	err := ta.run(context.Background(), []string{"apply-config", "--dry-run", "n1", "--inventory", ta.inventory})
	if err == nil || !strings.Contains(err.Error(), "only a reinstall of n1 carries it out") || strings.Contains(err.Error(), "is refused: n1") {
		t.Errorf("err = %v, want the node's refusal", err)
	}
	err = ta.run(context.Background(), []string{"apply-config", "--dry-run", "n1", "n9", "--inventory", ta.inventory})
	if err == nil || err.Error() != "the config of 1 node is refused: n1; the dry run failed on 1 node: n9" {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(ta.stderr.String(), "chalkctl: n1: ") || strings.Contains(ta.stderr.String(), "n1: n1") {
		t.Errorf("stderr = %q", ta.stderr)
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "systemctl restart") || strings.HasPrefix(c, "mkfs") {
			t.Errorf("node ran %s", c)
		}
	}

	// A SystemDisk without a disk fails the image's schema before the node is asked.
	ta.stderr.Reset()
	writeFile(t, filepath.Join(filepath.Dir(ta.inventory), "nodes", "n1.yaml"), "apiVersion: storage.chalkos.dev/v1alpha1\nkind: SystemDisk\nmetadata: {name: system}\nspec: {}\n---\n"+n1Network)
	err = ta.run(context.Background(), []string{"apply-config", "--dry-run", "n1", "n9", "--inventory", ta.inventory})
	if err == nil || err.Error() != "the config of 1 node is refused: n1; the dry run failed on 1 node: n9" {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(ta.stderr.String(), "chalkctl: the config of n1: ") {
		t.Errorf("stderr = %q", ta.stderr)
	}
}

// Several nodes take a delivery and a dry run alone, each with its address as NODE=ADDR, and
// --dry-run goes without what applies or tries config.
func TestApplyConfigDryRunUsage(t *testing.T) {
	ta := newTestApp(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"n1", "n2", "--staged"}, "--discard, --confirm, --rollback, --staged and --diff take one node"},
		{[]string{"n1", "n2", "--try", "1m", "--no-confirm"}, "--no-confirm leaves one node's trial open; with several nodes, --until deliver does"},
		{[]string{"n1", "--until", "deliver", "--dry-run"}, "--until deliver delivers the config on trial"},
		{[]string{"n1", "--dry-run", "--diff"}, "--dry-run prints the diff too; drop --diff"},
		{[]string{"n1", "--dry-run", "--rollback"}, "--dry-run checks the inventory's config; --rollback and --confirm go without it"},
		{[]string{"n1", "--dry-run", "--confirm"}, "--dry-run checks the inventory's config; --rollback and --confirm go without it"},
		{[]string{"n1", "--dry-run", "--try", "1m"}, "--dry-run tries nothing; drop --try, --no-confirm and --ignore-unit"},
		{[]string{"n1", "--dry-run", "--kubernetes-share"}, "--dry-run checks config alone; deliver --kubernetes-share without it"},
		{[]string{"n1", "n2", "--dry-run", "--address", "127.0.0.1:1"}, "with several nodes, give each node's address as NODE=ADDR"},
	} {
		err := ta.run(context.Background(), append(append([]string{"apply-config"}, tc.args...), "--inventory", ta.inventory))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("apply-config %v: %v, want %q", tc.args, err, tc.want)
		}
	}
}

// A refusal of a node's config is one, also wrapped further or within another error about the
// node, and no other error about the node is.
func TestRefusesConfig(t *testing.T) {
	refusal := &layer.NodeError{Node: "r1", Err: errors.New("the config of r1: x"), Named: true, Refused: true}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{refusal, true},
		{fmt.Errorf("wrapped: %w", refusal), true},
		{&layer.NodeError{Node: "r1", Err: refusal}, true},
		{layer.Named("r1", errors.New("dial tcp: no route")), false},
		{errors.New("r1 is not in the inventory"), false},
	} {
		if got := refusesConfig(tc.err); got != tc.want {
			t.Errorf("refusesConfig(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
