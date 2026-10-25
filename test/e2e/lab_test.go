package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/lab"
)

// runChalklab runs chalklab, with 30 minutes for a create to install and bootstrap the nodes,
// and logs what it printed.
func runChalklab(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, os.Getenv("CHALKLAB_CHALKLAB"), args...).CombinedOutput()
	t.Logf("chalklab %s:\n%s", strings.Join(args, " "), out)
	return string(out), err
}

// startLab creates the lab of the fixture's lab file chalklab/<file>, whose metadata.name is name,
// through chalklab, which installs its nodes, and attaches to their VMs. chalklab destroy removes
// the lab when the test ends, after the consoles of a test that failed are logged. It returns the
// nodes at their forwarded ports, the lab, what chalklab create printed and the lab's state
// directory.
func startLab(t *testing.T, dir, name, file string, args ...string) (map[string]*node, *lab.Lab, string, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	state, err := lab.StateDir(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Read before destroy removes it, so a create that failed logs the consoles too.
		l, _ := lab.ReadLab(state)
		if t.Failed() && l != nil {
			for _, n := range l.Nodes {
				logConsoleTail(t, l.VMConfig(n).ConsolePath(), 200)
			}
		}
		if _, err := runChalklab(t, "destroy"); err != nil {
			t.Errorf("chalklab destroy: %v", err)
		}
		if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("chalklab destroy left %s: %v", state, err)
		}
		if l != nil {
			for _, n := range l.Nodes {
				if lab.Running(l.VMConfig(n)) {
					t.Errorf("the VM of %s runs after chalklab destroy", n.Name)
				}
			}
		}
	})
	create := []string{"create", "--lab", filepath.Join(fixtures, "chalklab", file), "--secrets", os.Getenv("CHALKLAB_SECRETS")}
	out, err := runChalklab(t, append(create, args...)...)
	if err != nil {
		t.Fatalf("chalklab create: %v", err)
	}
	l, err := lab.ReadLab(state)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nodes := map[string]*node{}
	for _, n := range l.Nodes {
		vm, err := lab.AttachVM(ctx, l.VMConfig(n))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(vm.Detach)
		nodes[n.Name] = &node{vm: vm, addr: fmt.Sprintf("127.0.0.1:%d", n.ChalkdPort)}
	}
	return nodes, l, out, state
}
