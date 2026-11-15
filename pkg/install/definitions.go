package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
)

// readDefinitions reads the *.conf files of a repart.d directory.
func readDefinitions(dir string) (map[string]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.conf"))
	if err != nil {
		return nil, err
	}
	defs := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		defs[filepath.Base(p)] = string(data)
	}
	return defs, nil
}

// writeDefinitions writes the system region's definitions to dir with STATE encrypted as the
// policy says. The image's definition always encrypts STATE. It refuses a definition with a key
// parseDefinition does not allow, before it writes any.
func writeDefinitions(dir string, defs map[string]string, policy string) error {
	parsed := make(map[string]definedPartition, len(defs))
	for name, text := range defs {
		p, err := parseDefinition(name, text)
		if err != nil {
			return err
		}
		parsed[name] = p
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	foundState := false
	for name, text := range defs {
		if parsed[name].label == stateLabel {
			foundState = true
			text = withEncryption(text, policy)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			return err
		}
	}
	if !foundState {
		return errors.New("the system region's definitions have no STATE partition")
	}
	return nil
}

func withEncryption(text, policy string) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "Encrypt" {
			continue
		}
		lines = append(lines, line)
	}
	if policy == storage.EncryptionTPM2 {
		lines = append(lines, "Encrypt=tpm2")
	}
	return strings.Join(lines, "\n") + "\n"
}

// createSystemRegion writes the definitions with STATE encrypted as policy says and runs
// systemd-repart with them on dev, with the extra arguments before dev.
func (i *Installer) createSystemRegion(ctx context.Context, dev string, defs map[string]string, policy string, extra ...string) error {
	dir := filepath.Join(i.WorkDir, "system")
	if err := writeDefinitions(dir, defs, policy); err != nil {
		return err
	}
	args := append([]string{"--dry-run=no", "--seed=random", "--definitions=" + dir, "--tpm2-pcrs=7"}, extra...)
	_, err := tool.Run(ctx, i.Run, "systemd-repart", append(args, dev)...)
	return err
}

// layOut creates the system region on the wiped target with the image's definitions, STATE
// encrypted as the node's policy says. Partition UUIDs are random: a disk written from the same
// image must not share them.
func (i *Installer) layOut(ctx context.Context, target storage.BlockDisk, defs map[string]string, policy string) error {
	if err := i.change("lay out the target disk"); err != nil {
		return err
	}
	if err := i.createSystemRegion(ctx, target.Device, defs, policy, "--empty=require"); err != nil {
		return fmt.Errorf("lay out the target disk %s: %w", target, err)
	}
	return nil
}
