package chalkctl

import (
	"context"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config/slot"
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
		"  effects:  kubernetes: restart chalkos-kubernetes.service kubelet.service (rule)\n" +
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
// others, and the command fails naming it.
func TestApplyConfigDryRunOfSeveralNodes(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	writeFile(t, ta.inventory, strings.Replace(testInventoryYAML, "10.0.0.11", addr, 1))
	err := ta.run(context.Background(), []string{"apply-config", "--dry-run", "n9", "n1", "--inventory", ta.inventory})
	if err == nil || err.Error() != "the config of 1 node is refused: n9" {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(ta.stderr.String(), "chalkctl: n9: ") || !strings.Contains(ta.stdout.String(), "n1: config none → "+ta.configVersion(t)) {
		t.Errorf("stdout = %q, stderr = %q", ta.stdout, ta.stderr)
	}
}

// apply-config delivers to one node, and --dry-run goes without what applies or tries config.
func TestApplyConfigDryRunUsage(t *testing.T) {
	ta := newTestApp(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"n1", "n2"}, "apply-config delivers to one node; --dry-run checks several, one after the other"},
		{[]string{"n1", "--dry-run", "--staged"}, "--dry-run checks config for the running image; --staged goes without it"},
		{[]string{"n1", "--dry-run", "--diff"}, "--dry-run prints the diff too; drop --diff"},
		{[]string{"n1", "--dry-run", "--try", "1m"}, "--dry-run tries nothing; drop --try, --no-confirm and --ignore-unit"},
		{[]string{"n1", "--dry-run", "--kubernetes-share"}, "--dry-run checks config alone; deliver --kubernetes-share without it"},
		{[]string{"n1", "n2", "--dry-run", "--address", "127.0.0.1:1"}, "--address reaches one node; check the nodes one by one"},
	} {
		err := ta.run(context.Background(), append(append([]string{"apply-config"}, tc.args...), "--inventory", ta.inventory))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("apply-config %v: %v, want %q", tc.args, err, tc.want)
		}
	}
}
