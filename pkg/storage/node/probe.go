package node

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/tool"
)

// What blkid reports as the TYPE of a LUKS device and of an ext4 file system.
const (
	ContentLUKS = "crypto_LUKS"
	ContentExt4 = "ext4"
)

// Probe returns what blkid finds on a device, such as PTTYPE and TYPE; empty when it finds
// nothing.
func Probe(ctx context.Context, run tool.Runner, dev string) (map[string]string, error) {
	out, err := run.Run(ctx, "blkid", "-p", "-o", "export", dev)
	if foundNothing(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("probe the disk %s: %w", dev, err)
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	return props, nil
}

// Content returns the TYPE blkid finds on a device once udev processed it, such as crypto_LUKS
// or ext4; empty when it finds nothing.
func Content(ctx context.Context, run tool.Runner, dev string) (string, error) {
	if _, err := run.Run(ctx, "udevadm", "settle", "--timeout=30"); err != nil {
		return "", err
	}
	out, err := run.Run(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", dev)
	if foundNothing(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("probe %s: %w", dev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// foundNothing reports whether blkid failed because it found no signature: it exits with 2 and
// says nothing. It also exits with 2 when it cannot read the device, but then it complains.
func foundNothing(err error) bool {
	var te *tool.Error
	return errors.As(err, &te) && te.Code == 2 && strings.TrimSpace(te.Stderr) == ""
}
