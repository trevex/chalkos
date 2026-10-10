package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// PartsRequest installs from the installer: the role image's parts are streamed and written to
// a target disk, which is laid out from the role's definitions.
type PartsRequest struct {
	Request
	// Target is the disk the node is installed onto.
	Target storage.Ref
	// Image describes the role image, whose store, hash tree, UKI and boot loader follow in
	// Parts, in this order.
	Image upgrade.Header
	Parts io.Reader
	// SystemDefinitions are the role image's repart definitions of the system region, by file
	// name. The installer's own may differ: each role chooses its partition sizes.
	SystemDefinitions map[string]string
	// WipeDisk allows replacing whatever the target holds, including an installed node.
	WipeDisk bool
}

// FromParts installs the node onto the target disk from the role image's parts. Each step finds
// what an earlier, interrupted attempt did and continues from there:
//
//  1. Classify the target: an empty disk, or any with WipeDisk, is wiped and laid out anew; one
//     that holds what an earlier attempt left is continued; anything else is refused.
//  2. Lay out the system region with one systemd-repart run of the role's definitions: the ESP,
//     slot A, slot B as _empty, and STATE.
//  3. Write slot A with the upgrade's slot writer: retired, written, verified from the disk, and
//     activated with the UUIDs derived from the root hash and the version's labels.
//  4. Write the UKI to the ESP once it passed the upgrade's checks, then receive and check the
//     boot loader.
//  5. Open STATE, apply the storage section, enroll the fallback key and write the identity.
//  6. Write the boot loader, add the UEFI boot entry and boot the target next, and write the
//     installed marker last.
//
// Until the boot loader is written, firmware finds nothing to boot on the target, so an
// interrupted install never boots a node half installed.
func (i *Installer) FromParts(ctx context.Context, req PartsRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	h := req.Image
	if err := h.Validate(); err != nil {
		return fmt.Errorf("the image: %w", err)
	}
	if h.BootLoaderSize == 0 {
		return errors.New("the image brings no boot loader, which an install writes to the ESP")
	}
	if i.Loader == "" {
		return errors.New("no UEFI boot loader path is known for this architecture")
	}
	l, err := parseLayout(req.SystemDefinitions)
	if err != nil {
		return err
	}
	if err := l.fits(h); err != nil {
		return err
	}
	target, err := i.Host.Resolve(req.Target)
	if err != nil {
		return fmt.Errorf("resolve the target disk %s: %w", req.Target, err)
	}
	if err := i.refuseBootDisk(target); err != nil {
		return err
	}
	// An install that a crashed chalkd left behind holds the target's STATE.
	if err := i.closeState(ctx); err != nil {
		return err
	}
	// From here on the steps change the disk: a client that goes away must not kill a tool while
	// it writes. Its stream ends, which stops the install between steps.
	ctx = context.WithoutCancel(ctx)
	err = i.fromParts(ctx, target, l, req)
	if err != nil {
		// Leave the target free, so the install can run again.
		if cerr := i.closeState(ctx); cerr != nil {
			return fmt.Errorf("%w; releasing the target's STATE failed too: %v", err, cerr)
		}
	}
	return err
}

func (i *Installer) fromParts(ctx context.Context, target storage.BlockDisk, l layout, req PartsRequest) error {
	h := req.Image
	fresh, err := i.classify(ctx, target, l, req)
	if err != nil {
		return err
	}
	if fresh {
		if err := i.wipe(ctx, target); err != nil {
			return err
		}
		if err := i.layOut(ctx, target, req.SystemDefinitions, req.Section.Encryption); err != nil {
			return err
		}
	}
	slots := i.slotWriter(target)
	parts, err := slots.ReadTable(ctx)
	if err != nil {
		return err
	}
	pairs, err := upgrade.StoreSlots(target.String(), parts)
	if err != nil {
		return err
	}
	if err := writeSlot(ctx, slots, pairs[0], h, req.Parts); err != nil {
		return err
	}
	table, err := i.readTable(ctx, target.Device)
	if err != nil {
		return err
	}
	esp, err := table.findType(typeESP)
	if err != nil {
		return err
	}
	loader, err := i.writeUKI(ctx, esp, h, req.Parts)
	if err != nil {
		return err
	}
	if err := i.installOn(ctx, target, req.SystemDefinitions, req.Request); err != nil {
		return err
	}
	if err := i.writeBootLoader(ctx, esp, loader); err != nil {
		return err
	}
	if err := i.addBootEntry(ctx, target); err != nil {
		return err
	}
	return i.markInstalled()
}

// classify tells whether the target is laid out anew: when it is empty or the request wipes it.
// A target that holds what an earlier attempt of this install left is continued, and anything
// else refused, naming what it holds.
func (i *Installer) classify(ctx context.Context, target storage.BlockDisk, l layout, req PartsRequest) (bool, error) {
	if req.WipeDisk {
		return true, nil
	}
	out, err := i.Run.Run(ctx, "blkid", "-p", "-o", "export", target.Device)
	var te *node.ToolError
	// blkid exits with 2 and says nothing when it finds nothing.
	if errors.As(err, &te) && te.Code == 2 && strings.TrimSpace(te.Stderr) == "" {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe the target disk %s: %w", target.Device, err)
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	if props["PTTYPE"] != "gpt" || props["TYPE"] != "" {
		return false, fmt.Errorf("the target disk %s carries data (%s); pass --wipe-disk to replace it", target, strings.TrimSpace(props["PTTYPE"]+" "+props["TYPE"]))
	}
	table, err := i.readTable(ctx, target.Device)
	if err != nil {
		return false, err
	}
	if len(table.Partitions) == 0 {
		return true, nil
	}
	if err := l.check(table, req.Section); err != nil {
		return false, fmt.Errorf("the target disk %s is not one an install of this role left: %w; pass --wipe-disk to replace it", target, err)
	}
	parts, err := i.slotWriter(target).ReadTable(ctx)
	if err != nil {
		return false, err
	}
	pairs, err := upgrade.StoreSlots(target.String(), parts)
	if err != nil {
		return false, err
	}
	if a := pairs[0]; a.Version() != "" && !a.Holds(req.Image.RootHash) {
		return false, fmt.Errorf("slot A of the target disk %s holds version %s of another store; pass --wipe-disk to replace it", target, a.Version())
	}
	return false, i.checkState(ctx, target, table, req.Section.Encryption)
}

// checkState refuses a target whose STATE belongs to an installed node or to another machine:
// one that holds the installed marker, or that this machine's TPM does not open. A STATE that
// holds no file system yet is installed over.
func (i *Installer) checkState(ctx context.Context, target storage.BlockDisk, table partitionTable, policy string) error {
	state, err := table.find(stateLabel, storage.PartitionType(stateLabel))
	if err != nil {
		return err
	}
	found, err := content(ctx, i.Run, state.Node)
	if err != nil {
		return err
	}
	switch found {
	case "":
		return nil
	case contentLUKS, contentExt4:
	default:
		return fmt.Errorf("STATE on the target disk %s holds %q; pass --wipe-disk to replace it", target, found)
	}
	if err := i.storage(target).Open(ctx, stateMapperName, state.Node, i.StateDir, false, found == contentLUKS); err != nil {
		return fmt.Errorf("STATE on the target disk %s does not open on this machine, so it belongs to another node: %w; pass --wipe-disk to replace it", target, err)
	}
	installed, err := Installed(i.StateDir)
	if cerr := i.closeState(ctx); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if installed {
		return fmt.Errorf("the target disk %s holds an installed node; pass --wipe-disk to replace it", target)
	}
	return nil
}

// storage opens and changes the node's volumes on the disk.
func (i *Installer) storage(disk storage.BlockDisk) *node.Storage {
	return &node.Storage{
		Run:        i.Run,
		Host:       i.Host,
		StateDir:   i.StateDir,
		BootDisk:   disk.Device,
		StatusFile: filepath.Join(i.WorkDir, "storage-status.json"),
	}
}

// slotWriter writes the target's store slots.
func (i *Installer) slotWriter(target storage.BlockDisk) *upgrade.SlotWriter {
	return &upgrade.SlotWriter{Run: i.Run, Disk: target.Device, OpenPartition: i.OpenPartition, Change: i.Change}
}

// wipe drops the target's partition tables and the signatures at its start: both GPT copies are
// zeroed, as is the disk's first MiB, where most file systems and volume managers keep theirs.
func (i *Installer) wipe(ctx context.Context, target storage.BlockDisk) (err error) {
	if err := i.change("wipe the target disk"); err != nil {
		return err
	}
	f, err := i.OpenDisk(ctx, target.Device)
	if errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("the target disk %s is in use: one of its partitions is mounted or held by another device", target)
	}
	if err != nil {
		return fmt.Errorf("open the target disk: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zero := make([]byte, mib)
	if _, err := f.WriteAt(zero, 0); err != nil {
		return fmt.Errorf("clear the start of the target disk: %w", err)
	}
	if size := int64(target.Identity.Size); size >= 2*mib {
		if _, err := f.WriteAt(zero, size-mib); err != nil {
			return fmt.Errorf("clear the end of the target disk: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	// The kernel still knows the partitions the disk had.
	return f.RereadPartitions()
}

// layOut creates the system region on the wiped target with the role's definitions, STATE
// encrypted as the node's policy says. Partition UUIDs are random: a disk written from the same
// image must not share them.
func (i *Installer) layOut(ctx context.Context, target storage.BlockDisk, defs map[string]string, policy string) error {
	dir := filepath.Join(i.WorkDir, "system")
	if err := writeDefinitions(dir, defs, policy); err != nil {
		return err
	}
	if err := i.change("lay out the target disk"); err != nil {
		return err
	}
	if _, err := i.Run.Run(ctx, "systemd-repart", "--dry-run=no", "--empty=allow", "--seed=random", "--definitions="+dir, "--tpm2-pcrs=7", target.Device); err != nil {
		return fmt.Errorf("lay out the target disk %s: %w", target, err)
	}
	return nil
}

// writeSlot writes the image's store into the slot, unless the slot holds it already: retired
// first, so a slot written in part never carries the UUIDs a UKI finds its store by, and
// activated once the store verifies from the disk.
func writeSlot(ctx context.Context, w *upgrade.SlotWriter, slot upgrade.Slot, h upgrade.Header, parts io.Reader) error {
	if slot.Holds(h.RootHash) && slot.Version() == h.Version && w.Holds(slot, h) {
		log.Printf("slot A holds the store of %s already", h.Version)
		if _, err := io.CopyN(io.Discard, parts, h.StoreSize+h.VeritySize); err != nil {
			return fmt.Errorf("receive the image: %w", err)
		}
		return nil
	}
	if err := slot.Fits(h); err != nil {
		return err
	}
	if err := w.Retire(ctx, slot); err != nil {
		return err
	}
	if _, err := w.Write(h, slot, parts); err != nil {
		return err
	}
	return w.Activate(ctx, slot, h)
}

// ukiTempPrefix starts the name the UKI is written under before it gets its own; systemd-boot
// ignores files whose names start with a dot.
const ukiTempPrefix = ".install-"

// writeUKI writes the image's UKI to the ESP, checked as an upgrade checks it, and then receives
// the boot loader, which it checks and returns. The UKI has no tries counter: the first boot has
// nothing to fall back to.
func (i *Installer) writeUKI(ctx context.Context, esp partition, h upgrade.Header, parts io.Reader) (_ []byte, err error) {
	dir, unmount, err := i.mountESP(ctx, esp)
	if err != nil {
		return nil, err
	}
	defer func() {
		if uerr := unmount(); err == nil {
			err = uerr
		}
	}()
	linux := filepath.Join(dir, "EFI", "Linux")
	if err := os.MkdirAll(linux, 0o755); err != nil {
		return nil, err
	}
	if err := i.change("write the UKI"); err != nil {
		return nil, err
	}
	tmp := filepath.Join(linux, ukiTempPrefix+h.Version+".efi")
	loader, err := i.receiveUKI(tmp, h, parts)
	if err != nil {
		if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			log.Printf("remove %s: %v", tmp, rerr)
		}
		return nil, err
	}
	name := h.ImageID + "_" + h.Version + ".efi"
	if err := i.change("name the UKI " + name); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, filepath.Join(linux, name)); err != nil {
		return nil, fmt.Errorf("name the UKI %s: %w", name, err)
	}
	return loader, nil
}

func (i *Installer) receiveUKI(tmp string, h upgrade.Header, parts io.Reader) ([]byte, error) {
	if _, err := upgrade.ReceiveUKI(tmp, h, parts, i.EFIVars); err != nil {
		return nil, err
	}
	return i.receiveBootLoader(h, parts)
}

// receiveBootLoader receives the boot loader, the image's last part, and checks it: its SHA-256,
// that it is built for this machine, and with Secure Boot enforced, its signature.
func (i *Installer) receiveBootLoader(h upgrade.Header, parts io.Reader) ([]byte, error) {
	loader := make([]byte, h.BootLoaderSize)
	if _, err := io.ReadFull(parts, loader); err != nil {
		return nil, fmt.Errorf("receive the boot loader: %w", err)
	}
	if n, _ := parts.Read(make([]byte, 1)); n > 0 {
		return nil, errors.New("the image's parts are longer than its header says")
	}
	if sum := sha256.Sum256(loader); !bytes.Equal(sum[:], h.BootLoaderSHA256) {
		return nil, fmt.Errorf("the boot loader's SHA-256 is %x, want %x", sum, h.BootLoaderSHA256)
	}
	f, err := pe.NewFile(bytes.NewReader(loader))
	if err != nil {
		return nil, fmt.Errorf("the boot loader: %w", err)
	}
	if want := loaderMachine(i.Loader); f.Machine != want {
		return nil, fmt.Errorf("the boot loader is built for the machine type %#x, not this machine's %#x", f.Machine, want)
	}
	if err := upgrade.CheckSecureBoot(bytes.NewReader(loader), h.BootLoaderSize, i.EFIVars, "boot loader"); err != nil {
		return nil, err
	}
	return loader, nil
}

// loaderMachine is the PE machine type of the boot loader firmware starts from the path.
func loaderMachine(loader string) uint16 {
	switch {
	case strings.HasSuffix(loader, `\BOOTX64.EFI`):
		return pe.IMAGE_FILE_MACHINE_AMD64
	case strings.HasSuffix(loader, `\BOOTAA64.EFI`):
		return pe.IMAGE_FILE_MACHINE_ARM64
	}
	return 0
}

// writeBootLoader writes the boot loader to the ESP's removable-media path, where firmware finds
// it without a boot entry, under a temporary name first.
func (i *Installer) writeBootLoader(ctx context.Context, esp partition, loader []byte) (err error) {
	dir, unmount, err := i.mountESP(ctx, esp)
	if err != nil {
		return err
	}
	defer func() {
		if uerr := unmount(); err == nil {
			err = uerr
		}
	}()
	path := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(strings.TrimPrefix(i.Loader, `\`), `\`, "/")))
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

// mountESP mounts the target's ESP in the install's own directory, where nothing else uses it,
// and returns the directory and the function that flushes the file system and unmounts it.
func (i *Installer) mountESP(ctx context.Context, esp partition) (string, func() error, error) {
	dir := filepath.Join(i.WorkDir, "esp")
	// A crashed chalkd may have left it mounted.
	if mounted, err := isMounted(i.MountInfo, dir); err != nil {
		return "", nil, err
	} else if mounted {
		if _, err := i.Run.Run(ctx, "umount", dir); err != nil {
			return "", nil, fmt.Errorf("unmount the ESP: %w", err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	if _, err := i.Run.Run(ctx, "mount", "-t", "vfat", "-o", "umask=0077", esp.Node, dir); err != nil {
		return "", nil, fmt.Errorf("mount the ESP: %w", err)
	}
	return dir, func() error {
		err := syncFS(dir)
		if _, uerr := i.Run.Run(ctx, "umount", dir); uerr != nil && err == nil {
			err = fmt.Errorf("unmount the ESP: %w", uerr)
		}
		return err
	}, nil
}

// syncFS flushes the file system holding dir: the boot loader reads the ESP without the
// kernel's caches.
func syncFS(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := unix.Syncfs(int(d.Fd())); err != nil {
		return fmt.Errorf("sync the ESP: %w", err)
	}
	return nil
}
