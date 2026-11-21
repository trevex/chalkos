package units

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/tool"
)

// ApplyLinks has udev read its rules and the Link documents' .link files again, and replays the
// add events of the network devices below sysClassNet that have a bus parent, the physical ports,
// waiting until udev handled them. Each add event also has systemd-sysctl apply the sysctl.d
// settings of the device again, which would undo what networkd and a CNI set on virtual devices,
// so those are left alone.
func ApplyLinks(ctx context.Context, r tool.Runner, sysClassNet string) error {
	if _, err := tool.Run(ctx, r, "udevadm", "control", "--reload"); err != nil {
		return err
	}
	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return err
	}
	var devices []string
	for _, e := range entries {
		d := filepath.Join(sysClassNet, e.Name())
		if _, err := os.Stat(filepath.Join(d, "device")); err == nil {
			devices = append(devices, d)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if len(devices) == 0 {
		return nil
	}
	_, err = tool.Run(ctx, r, "udevadm", append([]string{"trigger", "--action=add", "--settle"}, devices...)...)
	return err
}
