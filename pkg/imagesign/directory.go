package imagesign

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/uki"
)

// SignDirectory copies the image directory in to out and signs the boot loader and UKIs of the
// copy's disk images with the Secure Boot db key and its certificate: the raw image, which
// repart-output.json describes, and an ISO beside it, which <name>.iso.json describes, as the
// installer's directory holds. Disk images are copied without their holes and the other entries
// with symlinks resolved, so out holds no link into in, which stays as it is. It then checks the
// UKI and the boot loader of each disk image against cert, as firmware with cert in db would. out
// must not exist, and is removed again when anything fails. SignDirectory returns the names of
// the disk images it signed.
func SignDirectory(ctx context.Context, in, out, key, cert string) (_ []string, err error) {
	info, err := os.Stat(in)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is no image directory", in)
	}
	disks, err := diskImages(in)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	if err := os.Mkdir(out, 0o755); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(out)
		}
	}()
	certPEM, err := os.ReadFile(cert)
	if err != nil {
		return nil, err
	}
	certs, err := pki.ParseBundle(string(certPEM))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cert, err)
	}
	if err := copyTree(ctx, in, out); err != nil {
		return nil, err
	}
	var names []string
	for _, d := range disks {
		raw := filepath.Join(out, d.name)
		parts, err := image.ReadPartitions(filepath.Join(out, d.partitions))
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
	return names, nil
}

// diskImage is a disk image of an image directory and the file that describes its partitions.
type diskImage struct{ name, partitions string }

// diskImages finds the disk images of an image directory: its one raw image, which
// repart-output.json describes, and each ISO, which the .json file named after it describes.
func diskImages(dir string) ([]diskImage, error) {
	raws, err := filepath.Glob(filepath.Join(dir, "*.raw"))
	if err != nil || len(raws) != 1 {
		return nil, fmt.Errorf("want exactly one .raw image in %s, found %v", dir, raws)
	}
	disks := []diskImage{{filepath.Base(raws[0]), "repart-output.json"}}
	isos, err := filepath.Glob(filepath.Join(dir, "*.iso"))
	if err != nil {
		return nil, err
	}
	for _, iso := range isos {
		disks = append(disks, diskImage{filepath.Base(iso), filepath.Base(iso) + ".json"})
	}
	return disks, nil
}

// copyTree copies the entries of the directory src into dst with symlinks resolved: disk images
// without their holes, other files and directories by their content.
func copyTree(ctx context.Context, src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		info, err := os.Stat(from)
		if err != nil {
			return err
		}
		switch ext := filepath.Ext(e.Name()); {
		case info.IsDir():
			if err := os.Mkdir(to, 0o755); err != nil {
				return err
			}
			if err := copyTree(ctx, from, to); err != nil {
				return err
			}
		case ext == ".raw" || ext == ".iso":
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
