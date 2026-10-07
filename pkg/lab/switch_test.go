package lab

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSwitchStartsAndStops(t *testing.T) {
	if _, err := exec.LookPath("vde_switch"); err != nil {
		t.Skip("vde_switch not in PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "switch")
	s, err := StartSwitch(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("socket directory: %v, %v", info, err)
	}
	s.Stop()
	select {
	case <-s.exited:
	default:
		t.Error("the switch still runs after Stop")
	}
}
