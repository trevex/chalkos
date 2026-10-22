package imagesign

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/uki"
)

// SignDirectory copies the image directory in into a new directory next to out and signs the boot
// loader and UKIs of the copy's disk images with the Secure Boot db key and its certificate: the
// raw image, which repart-output.json describes, and an ISO beside it, which <name>.iso.json
// describes, as the installer's directory holds. Disk images are copied without their holes and
// the other entries with symlinks resolved, so the copy holds no link into in, which stays as it
// is. SignDirectory checks the UKI and the boot loader of each disk image against cert, as
// firmware with cert in db would, and only once every disk image is signed and verified does it
// rename the copy to out. out must not exist and its parent directory must, or SignDirectory
// refuses before copying anything; the copy is removed again on any later failure, including out
// appearing while it was being built. SignDirectory returns the names of the disk images it
// signed.
func SignDirectory(ctx context.Context, in, out, key, cert string) (_ []string, err error) {
	info, err := os.Stat(in)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is no image directory", in)
	}
	if _, err := os.Stat(out); err == nil {
		return nil, fmt.Errorf("%s exists; signing writes a new directory", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	parent := filepath.Dir(out)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return nil, err
	}
	if !parentInfo.IsDir() {
		return nil, fmt.Errorf("%s is no directory", parent)
	}
	disks, err := diskImages(in)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(key); err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(cert)
	if err != nil {
		return nil, err
	}
	certs, err := pki.ParseBundle(string(certPEM))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cert, err)
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(out)+"-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()
	if err := copyTree(ctx, in, tmp); err != nil {
		return nil, err
	}
	var names []string
	for _, d := range disks {
		raw := filepath.Join(tmp, d.name)
		parts, err := image.ReadPartitions(filepath.Join(tmp, d.partitions))
		if err != nil {
			return nil, err
		}
		esp, err := image.FindPartition(parts, "esp")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.partitions, err)
		}
		if err := SignImage(ctx, raw, esp.Offset, key, cert); err != nil {
			return nil, err
		}
		if err := verify(ctx, raw, esp.Offset, certs); err != nil {
			return nil, fmt.Errorf("%s: %w", d.name, err)
		}
		names = append(names, d.name)
	}
	if _, err := os.Stat(out); err == nil {
		return nil, fmt.Errorf("%s appeared while signing", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(tmp, out); err != nil {
		return nil, err
	}
	return names, nil
}

// diskImage is a disk image of an image directory and the file that describes its partitions.
type diskImage struct{ name, partitions string }

// diskImages finds the disk images of an image directory: its one raw image, which
// repart-output.json describes, and each ISO, which the .json file named after it describes. It
// checks that every disk's partitions file exists before SignDirectory copies anything.
func diskImages(dir string) ([]diskImage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var raws, isos []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".raw":
			raws = append(raws, e.Name())
		case ".iso":
			isos = append(isos, e.Name())
		}
	}
	if len(raws) != 1 {
		return nil, fmt.Errorf("want exactly one .raw image in %s, found %v", dir, raws)
	}
	disks := []diskImage{{raws[0], "repart-output.json"}}
	for _, iso := range isos {
		disks = append(disks, diskImage{iso, iso + ".json"})
	}
	for _, d := range disks {
		if _, err := os.Stat(filepath.Join(dir, d.partitions)); err != nil {
			return nil, err
		}
	}
	return disks, nil
}

// errNotFileOrDir is copyTree's error for a directory entry that is, once a symlink is resolved,
// neither a regular file nor a directory, such as a FIFO or a socket.
var errNotFileOrDir = errors.New("not a regular file or a directory")

// copyTree copies the entries of the directory src into dst with symlinks resolved: disk images
// without their holes, other files and directories by their content. It refuses a dangling
// symlink and any entry that resolves to neither a regular file nor a directory, rather than, for
// a FIFO, blocking on a reader that never comes.
func copyTree(ctx context.Context, src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		info, err := os.Stat(from)
		if err != nil {
			return fmt.Errorf("%s: %w", from, err)
		}
		switch {
		case info.IsDir():
			if err := os.Mkdir(to, 0o755); err != nil {
				return err
			}
			if err := copyTree(ctx, from, to); err != nil {
				return err
			}
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s: %w", from, errNotFileOrDir)
		case filepath.Ext(e.Name()) == ".raw" || filepath.Ext(e.Name()) == ".iso":
			if err := copySparse(ctx, from, to); err != nil {
				return err
			}
		default:
			if err := copyFile(from, to); err != nil {
				return err
			}
		}
	}
	return nil
}

// copySparse copies a disk image without materialising its holes, writable for signing.
func copySparse(ctx context.Context, src, dst string) error {
	if out, err := exec.CommandContext(ctx, "cp", "--sparse=always", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("cp %s: %w: %s", src, err, out)
	}
	return os.Chmod(dst, 0o644)
}

// copyFile copies a file's content to a new file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// verify checks the UKI and the boot loader on the image's ESP against the db certificates, as
// firmware with them in db would.
func verify(ctx context.Context, raw string, espOffset int64, certs []*x509.Certificate) error {
	dir, err := os.MkdirTemp("", "imagesign-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	b, err := image.ExtractBinaries(ctx, raw, espOffset, dir, true)
	if err != nil {
		return err
	}
	db := uki.Database{Certificates: certs}
	for _, f := range []struct{ what, path string }{{"UKI", b.UKI}, {"boot loader", b.BootLoader}} {
		if err := verifyFile(f.path, db); err != nil {
			return fmt.Errorf("Secure Boot would refuse the %s: %w", f.what, err)
		}
	}
	return nil
}

// verifyFile checks one EFI binary against the Secure Boot database db, as firmware would when
// deciding whether to run it.
func verifyFile(path string, db uki.Database) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return uki.VerifySignature(f, info.Size(), db, uki.Database{})
}
