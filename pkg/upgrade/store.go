package upgrade

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/uki"
)

// Store is an image's store as an upgrade reads it before the image boots: the slot's store
// mounted read-only through dm-verity, so what is read is what the root hash covers.
type Store struct {
	// root is where the store is mounted; its nix/store holds the image's store paths.
	root string
	// Toplevel is the image's NixOS system, as its UKI's init= names it, such as
	// /nix/store/...-nixos-system-.../, whose etc holds the image's /etc.
	Toplevel string
}

// storeMapping is the device-mapper name of the store an upgrade checks.
const storeMapping = "chalkos-upgrade-store"

// maxLinks bounds the symbolic links a path of the store follows, as the kernel's limit does.
const maxLinks = 40

// ReadFile reads the file at name, an absolute path in the image such as Toplevel+"/etc/...".
func (s Store) ReadFile(name string) ([]byte, error) {
	p, err := s.resolve(name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// ReadDir lists the directory at name, an absolute path in the image.
func (s Store) ReadDir(name string) ([]fs.DirEntry, error) {
	p, err := s.resolve(name)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(p)
}

// resolve finds name in the mounted store, following its symbolic links within the image: their
// absolute targets, such as /etc's links into /nix/store, name paths of the image, not of the
// running system.
func (s Store) resolve(name string) (string, error) {
	if !path.IsAbs(name) {
		return "", fmt.Errorf("%s is not an absolute path of the image", name)
	}
	logical, rest := "/", strings.Split(path.Clean(name), "/")[1:]
	for links := 0; len(rest) > 0; {
		next := path.Join(logical, rest[0])
		rest = rest[1:]
		host := filepath.Join(s.root, filepath.FromSlash(next))
		info, err := os.Lstat(host)
		if err != nil {
			return "", fmt.Errorf("%s in the image: %w", name, err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			logical = next
			continue
		}
		if links++; links > maxLinks {
			return "", fmt.Errorf("%s in the image: too many symbolic links", name)
		}
		target, err := os.Readlink(host)
		if err != nil {
			return "", err
		}
		if !path.IsAbs(target) {
			target = path.Join(logical, target)
		}
		rest = append(strings.Split(path.Clean(target), "/")[1:], rest...)
		logical = "/"
	}
	return filepath.Join(s.root, filepath.FromSlash(logical)), nil
}

// toplevel is the NixOS system a UKI's command line boots, the directory of its init=: the last
// one before "--", as the kernel reads it, past which the arguments are init's own.
func toplevel(cmdline string) (string, error) {
	init := ""
	for _, arg := range strings.Fields(cmdline) {
		if arg == "--" {
			break
		}
		if v, ok := strings.CutPrefix(arg, "init="); ok {
			init = v
		}
	}
	if init == "" {
		return "", errors.New("the UKI's command line names no init=")
	}
	if !path.IsAbs(init) || path.Base(init) == "/" {
		return "", fmt.Errorf("the UKI's init=%s is no absolute path", init)
	}
	return path.Dir(path.Clean(init)), nil
}

// storeDir is where the store an upgrade checks is mounted.
func (n *Node) storeDir() string {
	if n.StoreMount != "" {
		return n.StoreMount
	}
	return "/run/chalkd/upgrade-store"
}

// checkStore mounts the slot's store read-only through dm-verity with the image's root hash and
// passes it to n.CheckStore with the system the UKI at uki boots, then unmounts it. A refusal of
// n.CheckStore is a CheckError.
func (n *Node) checkStore(ctx context.Context, h Header, slot storeslot.Slot, ukiPath string) error {
	return n.mountStore(ctx, h.RootHash, slot, ukiPath, func(st Store) error {
		if err := n.CheckStore(st); err != nil {
			return &CheckError{Err: err}
		}
		return nil
	})
}

// ReadImage reads the store of the image with the root hash, which the inactive slot holds, such
// as the image staged to boot next: mounted read-only through dm-verity as an upgrade checks one,
// passed to read with the system a UKI on the ESP that boots it names, and unmounted again. The
// caller holds TableLock, if any, or leaves it unset. It fails for the running image and for one
// the inactive slot does not hold.
func (n *Node) ReadImage(ctx context.Context, rootHash []byte, read func(Store) error) error {
	running, err := uki.RunningHash(n.Cmdline)
	if err != nil {
		return err
	}
	if bytes.Equal(rootHash, running) {
		return fmt.Errorf("%x is the running image, not one in the inactive slot", rootHash)
	}
	parts, err := n.slots().ReadTable(ctx)
	if err != nil {
		return err
	}
	_, inactive, err := slots(parts, running)
	if err != nil {
		return err
	}
	if !inactive.Holds(rootHash) {
		return fmt.Errorf("the inactive slot does not hold the image %x", rootHash)
	}
	entries, err := esp.Entries(n.ESP)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(entries, func(e esp.Entry) bool { return bytes.Equal(e.RootHash, rootHash) })
	if i < 0 {
		return fmt.Errorf("no UKI on the ESP boots the image %x", rootHash)
	}
	ctx = context.WithoutCancel(ctx)
	n.closeStore(ctx, n.storeDir(), true, true)
	return n.mountStore(ctx, rootHash, inactive, filepath.Join(n.ESP, esp.LinuxDir, entries[i].File), read)
}

// mountStore mounts the slot's store read-only through dm-verity with the root hash and passes it
// to read with the system the UKI at ukiPath boots, then unmounts it.
func (n *Node) mountStore(ctx context.Context, rootHash []byte, slot storeslot.Slot, ukiPath string, read func(Store) error) error {
	f, err := os.Open(ukiPath)
	if err != nil {
		return err
	}
	img, err := uki.Read(f)
	f.Close()
	if err != nil {
		return err
	}
	system, err := toplevel(img.Cmdline)
	if err != nil {
		return err
	}
	dir := n.storeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := n.Run.Run(ctx, "veritysetup", "open", slot.Data.Node, storeMapping, slot.Verity.Node, hex.EncodeToString(rootHash)); err != nil {
		return fmt.Errorf("open the written store through dm-verity: %w", err)
	}
	mounted := false
	defer func() { n.closeStore(ctx, dir, mounted, false) }()
	if _, err := n.Run.Run(ctx, "mount", "-t", "erofs", "-o", "ro", "/dev/mapper/"+storeMapping, dir); err != nil {
		return fmt.Errorf("mount the written store: %w", err)
	}
	mounted = true
	return read(Store{root: dir, Toplevel: system})
}

// closeStore unmounts the checked store, when mounted, and closes its dm-verity device. Before an
// upgrade changes the disk, where neither is there unless an earlier check was cut short, it
// says nothing of failing to.
func (n *Node) closeStore(ctx context.Context, dir string, mounted, quiet bool) {
	if mounted {
		if _, err := n.Run.Run(ctx, "umount", dir); err != nil && !quiet {
			log.Printf("unmount the checked store: %v", err)
		}
	}
	if _, err := n.Run.Run(ctx, "veritysetup", "close", storeMapping); err != nil && !quiet {
		log.Printf("close the checked store: %v", err)
	}
}
