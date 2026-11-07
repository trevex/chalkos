// Package hostopstest holds a fake hostops.Ops for tests.
package hostopstest

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/tool"
)

// Fake keeps the host's mounts and device-mapper volumes in maps. Each operation first runs its
// command on the embedded Tools' runner, which records it and may fail it; one that succeeds
// changes the maps as the real tool changes the host. It is safe for concurrent use.
type Fake struct {
	hostops.Tools

	mu sync.Mutex
	// mounts holds the source mounted at each directory.
	mounts map[string]string
	// mappers holds the device behind each open device-mapper volume.
	mappers map[string]string
	// OnMount, when set, sees each mount whose command succeeded and may fail it, before the
	// mount is recorded.
	OnMount func(source, dir, options string) error
}

// New is a fake with nothing mounted and no volume open, which runs its commands on run.
func New(run tool.Runner) *Fake {
	return &Fake{Tools: hostops.Tools{Run: run}, mounts: map[string]string{}, mappers: map[string]string{}}
}

// SetMounted records source as mounted at dir.
func (f *Fake) SetMounted(source, dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounts[dir] = source
}

// SetMapped records the device-mapper volume name as open on dev.
func (f *Fake) SetMapped(name, dev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mappers[name] = dev
}

// Backing is the device behind the open device-mapper volume name, and whether it is open.
func (f *Fake) Backing(name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dev, ok := f.mappers[name]
	return dev, ok
}

// MountedDirs holds the directories something is mounted at, sorted.
func (f *Fake) MountedDirs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.mounts))
}

// Holds reports whether a device whose name starts with dev, such as a partition of the disk
// dev, is mounted or backs an open device-mapper volume.
func (f *Fake) Holds(dev string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, src := range f.mounts {
		if strings.HasPrefix(src, dev) {
			return true
		}
	}
	for _, backing := range f.mappers {
		if strings.HasPrefix(backing, dev) {
			return true
		}
	}
	return false
}

func (f *Fake) Mount(ctx context.Context, source, dir, fsType, options string) error {
	if err := f.Tools.Mount(ctx, source, dir, fsType, options); err != nil {
		return err
	}
	if f.OnMount != nil {
		if err := f.OnMount(source, dir, options); err != nil {
			return err
		}
	}
	f.SetMounted(source, dir)
	return nil
}

// Unmount fails as umount does for a directory nothing is mounted at.
func (f *Fake) Unmount(ctx context.Context, dir string) error {
	if err := f.Tools.Unmount(ctx, dir); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.mounts[dir]; !ok {
		return &tool.Error{Command: "umount", Code: 32, Stderr: "umount: " + dir + ": not mounted."}
	}
	delete(f.mounts, dir)
	return nil
}

func (f *Fake) Mounted(dir string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.mounts[dir]
	return ok, nil
}

func (f *Fake) Mapped(name string) bool {
	_, ok := f.Backing(name)
	return ok
}

func (f *Fake) AttachLUKS(ctx context.Context, name, dev string, o hostops.LUKSOptions) error {
	if err := f.Tools.AttachLUKS(ctx, name, dev, o); err != nil {
		return err
	}
	f.SetMapped(name, dev)
	return nil
}

// DetachLUKS succeeds for a volume that is not open, as systemd-cryptsetup does.
func (f *Fake) DetachLUKS(ctx context.Context, name string) error {
	if err := f.Tools.DetachLUKS(ctx, name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.mappers, name)
	return nil
}

// OpenVerity records the volume as backed by the data device.
func (f *Fake) OpenVerity(ctx context.Context, name, data, hash string, rootHash []byte) error {
	if err := f.Tools.OpenVerity(ctx, name, data, hash, rootHash); err != nil {
		return err
	}
	f.SetMapped(name, data)
	return nil
}

// CloseVerity fails as veritysetup does for a volume that is not open.
func (f *Fake) CloseVerity(ctx context.Context, name string) error {
	if err := f.Tools.CloseVerity(ctx, name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.mappers[name]; !ok {
		return &tool.Error{Command: "veritysetup", Code: 4, Stderr: "Device " + name + " is not active."}
	}
	delete(f.mappers, name)
	return nil
}
