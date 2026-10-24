package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/gpt"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// writeUKI writes the image's UKI to the ESP, checked as an upgrade checks it, and then receives
// the boot loader, which it checks and returns. The UKI is the image's first generation, with no
// tries counter: the first boot has nothing to fall back to.
func (i *Installer) writeUKI(ctx context.Context, espPart gpt.Partition, h upgrade.Header, parts io.Reader) (loader []byte, err error) {
	err = i.withESP(ctx, espPart, func(dir string) error {
		if err := i.change("write the UKI"); err != nil {
			return err
		}
		tmp, err := esp.Stage(dir, h.RootHash)
		if err != nil {
			return err
		}
		if loader, err = i.receiveUKI(tmp, h, parts); err != nil {
			if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				log.Printf("remove %s: %v", tmp, rerr)
			}
			return err
		}
		name := esp.EntryName(h.ImageID, 1)
		if err := i.removeOtherUKIs(dir, h.ImageID, name, tmp); err != nil {
			return err
		}
		return esp.Name(dir, tmp, name, i.Change)
	})
	return loader, err
}

// removeOtherUKIs removes from the ESP mounted at dir every UKI of the image but the one named
// keep, and UKIs an earlier install left half written but tmp. An install continued over an
// earlier attempt finds that attempt's files there, and systemd-boot would boot a UKI of the
// image of a higher generation before the one the install writes.
func (i *Installer) removeOtherUKIs(dir, imageID, keep, tmp string) error {
	remove, err := esp.Leftovers(dir, tmp)
	if err != nil {
		return err
	}
	entries, err := esp.Entries(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.ImageID == imageID && e.File != keep {
			remove = append(remove, e.File)
		}
	}
	return esp.Remove(dir, remove, i.Change)
}

func (i *Installer) receiveUKI(tmp string, h upgrade.Header, parts io.Reader) ([]byte, error) {
	if _, err := upgrade.ReceiveUKI(tmp, h, parts, i.EFIVars); err != nil {
		return nil, err
	}
	return i.receiveBootLoader(h, parts)
}

// receiveBootLoader receives the boot loader, the image's last part, and checks it: its SHA-256,
// that it is built for the image's architecture, and with Secure Boot enforced, its signature.
func (i *Installer) receiveBootLoader(h upgrade.Header, parts io.Reader) ([]byte, error) {
	loader := make([]byte, h.BootLoaderSize)
	if _, err := io.ReadFull(parts, loader); err != nil {
		return nil, fmt.Errorf("receive the boot loader: %w", err)
	}
	if err := upgrade.ExpectEnd(parts, "boot loader"); err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(loader); !bytes.Equal(sum[:], h.BootLoaderSHA256) {
		return nil, fmt.Errorf("the boot loader's SHA-256 is %x, want %x", sum, h.BootLoaderSHA256)
	}
	if err := upgrade.CheckArchitecture(bytes.NewReader(loader), h, "boot loader"); err != nil {
		return nil, err
	}
	if err := upgrade.CheckSecureBoot(bytes.NewReader(loader), h.BootLoaderSize, i.EFIVars, "boot loader"); err != nil {
		return nil, err
	}
	return loader, nil
}

// writeBootLoader writes the boot loader to the ESP's removable-media path, where firmware finds
// it without a boot entry, under a temporary name first.
func (i *Installer) writeBootLoader(ctx context.Context, espPart gpt.Partition, loader []byte) error {
	return i.withESP(ctx, espPart, func(dir string) error {
		path := i.loaderFile(dir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := i.change("write the boot loader"); err != nil {
			return err
		}
		tmp := filepath.Join(filepath.Dir(path), ".boot-loader.tmp")
		if err := writeSynced(tmp, loader); err != nil {
			return fmt.Errorf("write the boot loader to the ESP: %w", err)
		}
		if err := i.change("name the boot loader"); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("write the boot loader to the ESP: %w", err)
		}
		return nil
	})
}

// writeSynced writes a file and syncs it.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// findESP finds the disk's ESP.
func (i *Installer) findESP(ctx context.Context, disk storage.BlockDisk) (gpt.Partition, error) {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return gpt.Partition{}, err
	}
	return table.findType(gpt.TypeESP)
}

// withESP mounts the target's ESP in the install's own directory, where nothing else uses it,
// passes the directory to use, and then flushes the file system and unmounts it.
func (i *Installer) withESP(ctx context.Context, espPart gpt.Partition, use func(dir string) error) (err error) {
	dir := filepath.Join(i.WorkDir, "esp")
	if err := i.releaseESP(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := i.Run.Run(ctx, "mount", "-t", "vfat", "-o", "umask=0077", espPart.Node, dir); err != nil {
		return fmt.Errorf("mount the ESP: %w", err)
	}
	defer func() {
		serr := esp.Sync(dir)
		if _, uerr := i.Run.Run(ctx, "umount", dir); uerr != nil && serr == nil {
			serr = fmt.Errorf("unmount the ESP: %w", uerr)
		}
		if err == nil {
			err = serr
		}
	}()
	return use(dir)
}

// releaseESP unmounts the target's ESP where a crashed chalkd may have left it mounted, which
// keeps the target in use.
func (i *Installer) releaseESP(ctx context.Context) error {
	dir := filepath.Join(i.WorkDir, "esp")
	mounted, err := isMounted(i.MountInfo, dir)
	if err != nil || !mounted {
		return err
	}
	if _, err := i.Run.Run(ctx, "umount", dir); err != nil {
		return fmt.Errorf("unmount the ESP: %w", err)
	}
	return nil
}

// removeBootLoader removes the boot loader from the ESP.
func (i *Installer) removeBootLoader(ctx context.Context, espPart gpt.Partition) error {
	return i.withESP(ctx, espPart, func(dir string) error {
		path := i.loaderFile(dir)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if err := i.change("remove the boot loader"); err != nil {
			return err
		}
		return os.Remove(path)
	})
}

// loaderFile is the boot loader's file on the ESP mounted at dir.
func (i *Installer) loaderFile(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(strings.TrimPrefix(i.loader(), `\`), `\`, "/")))
}

// randomizeESP gives the ESP a random partition UUID: systemd-boot reports it as the boot
// partition, and no other disk carrying the same image may match it.
func (i *Installer) randomizeESP(ctx context.Context, disk storage.BlockDisk) error {
	espPart, err := i.findESP(ctx, disk)
	if err != nil {
		return err
	}
	if _, err := i.sfdisk(disk.Device).Exec(ctx, "--part-uuid", disk.Device, strconv.Itoa(espPart.Number), gpt.NewUUID()); err != nil {
		return fmt.Errorf("set the ESP's partition UUID: %w", err)
	}
	return nil
}

// loader is where the image's systemd-boot sits on the ESP, the removable-media path of the
// architecture, as the UEFI boot entry names it; empty for an architecture chalkos builds no
// images for.
func (i *Installer) loader() string {
	name := uki.BootLoaderName(i.Architecture)
	if name == "" {
		return ""
	}
	return `\EFI\BOOT\` + name
}
