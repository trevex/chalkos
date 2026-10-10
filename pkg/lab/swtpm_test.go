package lab

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
}

func TestSWTPMStartsAndStops(t *testing.T) {
	requireTools(t, "swtpm")
	dir := noProcessLeft(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tpm, err := StartSWTPM(ctx, filepath.Join(dir, "tpm"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tpm.Stop)
	if _, err := os.Stat(tpm.Socket); err != nil {
		t.Fatalf("control socket missing: %v", err)
	}
	tpm.Stop()
	tpm.Stop() // a second Stop must be harmless
}
