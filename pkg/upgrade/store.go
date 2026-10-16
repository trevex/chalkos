package upgrade

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

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

// toplevel is the NixOS system a UKI's command line boots, the directory of its init=.
func toplevel(cmdline string) (string, error) {
	for _, arg := range strings.Fields(cmdline) {
		if init, ok := strings.CutPrefix(arg, "init="); ok && path.IsAbs(init) {
			return path.Dir(init), nil
		}
	}
	return "", errors.New("the UKI's command line names no init=")
}

// checkStore mounts the slot's store read-only through dm-verity with the image's root hash and
// passes it to n.CheckStore with the system the UKI at uki boots, then unmounts it.
func (n *Node) checkStore(ctx context.Context, h Header, slot Slot, ukiPath string) error {
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
	dir := n.StoreMount
	if dir == "" {
		dir = "/run/chalkd/upgrade-store"
	}
	// A check an earlier upgrade did not finish may have left the store open.
	n.closeStore(ctx, dir, true)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := n.Run.Run(ctx, "veritysetup", "open", slot.Data.Node, storeMapping, slot.Verity.Node, hex.EncodeToString(h.RootHash)); err != nil {
		return fmt.Errorf("open the written store through dm-verity: %w", err)
	}
	defer n.closeStore(ctx, dir, false)
	if _, err := n.Run.Run(ctx, "mount", "-t", "erofs", "-o", "ro", "/dev/mapper/"+storeMapping, dir); err != nil {
		return fmt.Errorf("mount the written store: %w", err)
	}
	return n.CheckStore(Store{root: dir, Toplevel: system})
}

// closeStore unmounts the checked store and closes its dm-verity device. Before a check, where
// neither is there unless an earlier check was cut short, it says nothing of failing to.
func (n *Node) closeStore(ctx context.Context, dir string, quiet bool) {
	if _, err := n.Run.Run(ctx, "umount", dir); err != nil && !quiet {
		log.Printf("unmount the checked store: %v", err)
	}
	if _, err := n.Run.Run(ctx, "veritysetup", "close", storeMapping); err != nil && !quiet {
		log.Printf("close the checked store: %v", err)
	}
}
