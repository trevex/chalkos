// Package hostops mounts file systems, opens LUKS and dm-verity volumes and probes devices for
// what they hold, the host operations the installer, an upgrade and the storage setup share. Ops
// is the seam tests replace with a fake that keeps the mounts and volumes in maps; Tools runs the
// tools, and is the one place their command lines are written.
package hostops

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/tool"
)

// Ops mounts file systems and opens and closes device-mapper volumes.
type Ops interface {
	// Mount mounts the file system of type fsType on source at dir, with the mount options,
	// comma-separated, if any.
	Mount(ctx context.Context, source, dir, fsType, options string) error
	Unmount(ctx context.Context, dir string) error
	// Mounted reports whether a file system is mounted at dir.
	Mounted(dir string) (bool, error)
	// Mapped reports whether the device-mapper volume name is open.
	Mapped(name string) bool
	// AttachLUKS unlocks the LUKS device dev as the volume name with its TPM2 key.
	AttachLUKS(ctx context.Context, name, dev string, o LUKSOptions) error
	// DetachLUKS closes the LUKS volume name; one that is not open is fine.
	DetachLUKS(ctx context.Context, name string) error
	// OpenVerity opens the data device as the volume name through dm-verity, checked against the
	// hash tree on the hash device with the root hash.
	OpenVerity(ctx context.Context, name, data, hash string, rootHash []byte) error
	CloseVerity(ctx context.Context, name string) error
}

// LUKSOptions say how systemd-cryptsetup unlocks a volume. The key always comes from the TPM.
type LUKSOptions struct {
	// Prompt lets systemd-cryptsetup ask for the fallback key when the TPM does not unseal the
	// volume; without it, only the TPM is tried.
	Prompt bool
	// ReadOnly attaches the volume read-only.
	ReadOnly bool
}

// String is the options as systemd-cryptsetup and crypttab take them.
func (o LUKSOptions) String() string {
	options := "tpm2-device=auto"
	if !o.Prompt {
		options += ",headless=true"
	}
	if o.ReadOnly {
		options += ",read-only"
	}
	return options
}

// Mapper is the device of the device-mapper volume name.
func Mapper(name string) string { return "/dev/mapper/" + name }

// Tools runs the host's tools: mount, umount, systemd-cryptsetup and veritysetup.
type Tools struct {
	Run tool.Runner
	// MountInfo is the mount table of the process's mount namespace.
	MountInfo string
	// DevRoot is where /dev is.
	DevRoot string
}

// System runs the tools on run against the running system's mount table and /dev.
func System(run tool.Runner) Tools {
	return Tools{Run: run, MountInfo: "/proc/self/mountinfo", DevRoot: "/dev"}
}

func (t Tools) Mount(ctx context.Context, source, dir, fsType, options string) error {
	args := []string{"-t", fsType}
	if options != "" {
		args = append(args, "-o", options)
	}
	_, err := tool.Run(ctx, t.Run, "mount", append(args, source, dir)...)
	return err
}

func (t Tools) Unmount(ctx context.Context, dir string) error {
	_, err := tool.Run(ctx, t.Run, "umount", dir)
	return err
}

func (t Tools) Mounted(dir string) (bool, error) {
	points, err := MountPoints(t.MountInfo)
	return points[dir], err
}

func (t Tools) Mapped(name string) bool {
	_, err := os.Stat(filepath.Join(t.DevRoot, "mapper", name))
	return err == nil
}

func (t Tools) AttachLUKS(ctx context.Context, name, dev string, o LUKSOptions) error {
	// "-" names no key file: the key comes from the TPM, or from the prompt.
	_, err := tool.Run(ctx, t.Run, "systemd-cryptsetup", "attach", name, dev, "-", o.String())
	return err
}

func (t Tools) DetachLUKS(ctx context.Context, name string) error {
	_, err := tool.Run(ctx, t.Run, "systemd-cryptsetup", "detach", name)
	return err
}

func (t Tools) OpenVerity(ctx context.Context, name, data, hash string, rootHash []byte) error {
	_, err := tool.Run(ctx, t.Run, "veritysetup", "open", data, name, hash, hex.EncodeToString(rootHash))
	return err
}

func (t Tools) CloseVerity(ctx context.Context, name string) error {
	_, err := tool.Run(ctx, t.Run, "veritysetup", "close", name)
	return err
}

// MountPoints holds the mount points of a mountinfo file. The paths chalkos mounts at need no
// escaping, so they are taken as the file has them.
func MountPoints(mountInfo string) (map[string]bool, error) {
	data, err := os.ReadFile(mountInfo)
	if err != nil {
		return nil, err
	}
	points := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 4 {
			points[fields[4]] = true
		}
	}
	return points, nil
}

// Release unmounts dir, if a file system is mounted there, and then closes the device-mapper
// volume mapper with close, if it is open: what a crashed process may have left behind. An empty
// mapper closes nothing. Errors name what is released.
func Release(ctx context.Context, o Ops, what, dir, mapper string, close func(ctx context.Context, name string) error) error {
	mounted, err := o.Mounted(dir)
	if err != nil {
		return err
	}
	if mounted {
		// It may have been unmounted since the mount table was read.
		if err := o.Unmount(ctx, dir); err != nil && !notMounted(err) {
			return fmt.Errorf("unmount %s: %w", what, err)
		}
	}
	if mapper != "" && o.Mapped(mapper) {
		if err := close(ctx, mapper); err != nil {
			return fmt.Errorf("close %s: %w", what, err)
		}
	}
	return nil
}

func notMounted(err error) bool {
	te, ok := errors.AsType[*tool.Error](err)
	return ok && strings.Contains(te.Stderr, "not mounted")
}
