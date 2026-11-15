package imagesign

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/internal/filecopy"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/secureboot"
)

// SignDirectory copies the image directory in to the new directory out, signs the UKI and boot
// loader of its raw image and of each ISO with the Secure Boot db key, and checks them against
// cert as firmware with cert in db would. It returns the names of the signed disk images. The copy
// resolves symlinks and keeps disk images sparse; it refuses before copying when out exists, and
// writes out as atomicfile.NewDir does, so out appears only once every image is signed.
func SignDirectory(ctx context.Context, in, out, key, cert string) ([]string, error) {
	info, err := os.Stat(in)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is no image directory", in)
	}
	if err := atomicfile.CheckNew(out); err != nil {
		return nil, err
	}
	disks, err := diskImages(in)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(key); err != nil {
		return nil, err
	}
	certs, err := readCerts(cert)
	if err != nil {
		return nil, err
	}
	var names []string
	err = atomicfile.NewDir(out, func(tmp string) error {
		if err := copyTree(ctx, in, tmp); err != nil {
			return err
		}
		for _, d := range disks {
			raw := filepath.Join(tmp, d.name)
			parts, err := image.ReadPartitions(filepath.Join(tmp, d.partitions))
			if err != nil {
				return err
			}
			esp, err := image.FindPartition(parts, "esp")
			if err != nil {
				return fmt.Errorf("%s: %w", d.partitions, err)
			}
			if err := SignImage(ctx, raw, esp.Offset, key, cert); err != nil {
				return err
			}
			if err := verify(ctx, raw, esp.Offset, certs); err != nil {
				return fmt.Errorf("%s: %w", d.name, err)
			}
			names = append(names, d.name)
		}
		return nil
	})
	if err != nil {
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
	raw, err := image.FindRaw(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	disks := []diskImage{{filepath.Base(raw), "repart-output.json"}}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".iso" {
			disks = append(disks, diskImage{e.Name(), e.Name() + ".json"})
		}
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
// symlink and any entry that resolves to neither a regular file nor a directory: a FIFO would
// block the copy.
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
			if err := filecopy.Sparse(ctx, from, to); err != nil {
				return err
			}
		default:
			if err := filecopy.File(from, to, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
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
	for _, f := range []struct{ what, path string }{{"UKI", b.UKI}, {"boot loader", b.BootLoader}} {
		if err := verifyFile(f.path, certs); err != nil {
			return fmt.Errorf("Secure Boot would refuse the %s: %w", f.what, err)
		}
	}
	return nil
}

// verifyFile checks one EFI binary against the db certificates, as firmware would when deciding
// whether to run it.
func verifyFile(path string, certs []*x509.Certificate) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return secureboot.VerifyAgainst(f, info.Size(), certs)
}

// readCerts reads the PEM certificates of a db signer.
func readCerts(cert string) ([]*x509.Certificate, error) {
	certPEM, err := os.ReadFile(cert)
	if err != nil {
		return nil, err
	}
	certs, err := pki.ParseBundle(string(certPEM))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cert, err)
	}
	return certs, nil
}
