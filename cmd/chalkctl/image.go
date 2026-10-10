package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagesign"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// diskImage is what install and upgrade send of a disk image: its store, the store's hash tree,
// its UKI and, for an install, its boot loader, both signed when chalkctl was given the key, with
// what the UKI says of the image.
type diskImage struct {
	raw   *os.File
	store image.Store
	// uki and bootLoader are the files of the UKI and the boot loader, "" for an upgrade, and info
	// what the UKI says.
	uki, bootLoader string
	info            uki.Image
	// header describes the image to the nodes.
	header *nodev1.ImageHeader
	// cleanup removes the copies of the UKI and the boot loader.
	cleanup func()
}

// openImage reads an image for an install, with its boot loader, or an upgrade: a raw image with
// repart-output.json next to it, or the directory nix build makes. The UKI and the boot loader
// are copied off its ESP into a temporary directory and signed there with key and cert when they
// are given.
func openImage(ctx context.Context, path, key, cert string, bootLoader bool) (_ *diskImage, err error) {
	if (key == "") != (cert == "") {
		return nil, errors.New("signing needs both --sign-key and --sign-cert")
	}
	raw, _, err := imageFiles(path)
	if err != nil {
		return nil, err
	}
	parts, err := image.ReadPartitions(filepath.Join(filepath.Dir(raw), "repart-output.json"))
	if err != nil {
		return nil, err
	}
	esp, err := image.FindPartition(parts, "esp")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(raw)
	if err != nil {
		return nil, err
	}
	img := &diskImage{raw: f, cleanup: func() {}}
	defer func() {
		if err != nil {
			img.Close()
		}
	}()
	if img.store, err = image.ReadStore(f, parts); err != nil {
		return nil, fmt.Errorf("%s: %w", raw, err)
	}
	dir, err := os.MkdirTemp("", "chalkctl-image-")
	if err != nil {
		return nil, err
	}
	img.cleanup = func() { os.RemoveAll(dir) }
	if img.uki, err = imagesign.ExtractUKI(ctx, raw, esp.Offset, dir); err != nil {
		return nil, err
	}
	binaries := []string{img.uki}
	if bootLoader {
		if img.bootLoader, err = imagesign.ExtractBootLoader(ctx, raw, esp.Offset, dir); err != nil {
			return nil, err
		}
		binaries = append(binaries, img.bootLoader)
	}
	if key != "" {
		for _, b := range binaries {
			if err := imagesign.SignFile(ctx, b, key, cert); err != nil {
				return nil, err
			}
		}
	}
	u, err := os.Open(img.uki)
	if err != nil {
		return nil, err
	}
	defer u.Close()
	if img.info, err = uki.Read(u); err != nil {
		return nil, fmt.Errorf("the image's UKI: %w", err)
	}
	if hash, err := img.info.UsrHash(); err != nil || !bytes.Equal(hash, img.store.RootHash) {
		return nil, errors.New("the image's UKI boots another store than the image holds")
	}
	if !upgrade.VersionPattern.MatchString(img.info.Version()) {
		return nil, fmt.Errorf("the image's version %q is not one chalkos installs: 1 to 23 characters of a-z, 0-9, '.', '~', '^' and '-'", img.info.Version())
	}
	if img.info.Role() == "" || img.info.Cluster() == "" {
		return nil, errors.New("the image names no role or cluster in its os-release; it is no role image of a cluster")
	}
	h := &nodev1.ImageHeader{
		Version:  img.info.Version(),
		ImageId:  img.info.ID(),
		Cluster:  img.info.Cluster(),
		Role:     img.info.Role(),
		RootHash: img.store.RootHash,
	}
	if h.Store, err = sum(img.store.Data); err != nil {
		return nil, err
	}
	if h.HashTree, err = sum(img.store.HashTree); err != nil {
		return nil, err
	}
	if h.Uki, err = sum(u); err != nil {
		return nil, err
	}
	if bootLoader {
		b, err := os.Open(img.bootLoader)
		if err != nil {
			return nil, err
		}
		defer b.Close()
		if h.BootLoader, err = sum(b); err != nil {
			return nil, err
		}
	}
	img.header = h
	return img, nil
}

func sum(r io.Reader) (*nodev1.ImagePart, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return nil, err
	}
	return &nodev1.ImagePart{Size: uint64(n), Sha256: h.Sum(nil)}, nil
}

// checkSignature checks the UKI and the boot loader against a db certificate, as firmware with
// it in db would.
func (img *diskImage) checkSignature(certPEM string) error {
	certs, err := pki.ParseBundle(certPEM)
	if err != nil {
		return fmt.Errorf("the db certificate: %w", err)
	}
	for _, b := range []struct {
		what, file string
		part       *nodev1.ImagePart
	}{
		{"UKI", img.uki, img.header.Uki},
		{"boot loader", img.bootLoader, img.header.BootLoader},
	} {
		if b.file == "" {
			continue
		}
		f, err := os.Open(b.file)
		if err != nil {
			return err
		}
		err = uki.VerifySignature(f, int64(b.part.Size), uki.Database{Certificates: certs}, uki.Database{})
		f.Close()
		if err != nil {
			return fmt.Errorf("Secure Boot would refuse the image's %s: %w", b.what, err)
		}
	}
	return nil
}

// size is how many bytes of the image follow its header.
func (img *diskImage) size() uint64 {
	h := img.header
	return h.Store.GetSize() + h.HashTree.GetSize() + h.Uki.GetSize() + h.BootLoader.GetSize()
}

// parts are the image's store, hash tree, UKI and boot loader as install and upgrade stream them.
func (img *diskImage) parts() (io.Reader, func(), error) {
	u, err := os.Open(img.uki)
	if err != nil {
		return nil, nil, err
	}
	data, tree := img.store.Data, img.store.HashTree
	readers := []io.Reader{io.NewSectionReader(data, 0, data.Size()), io.NewSectionReader(tree, 0, tree.Size()), u}
	closers := []io.Closer{u}
	if img.bootLoader != "" {
		b, err := os.Open(img.bootLoader)
		if err != nil {
			u.Close()
			return nil, nil, err
		}
		readers = append(readers, b)
		closers = append(closers, b)
	}
	return io.MultiReader(readers...), func() {
		for _, c := range closers {
			c.Close()
		}
	}, nil
}

func (img *diskImage) Close() {
	img.raw.Close()
	img.cleanup()
}

// checkInstallImage checks the image against the node it installs: of the node's role, and
// checked against the cluster as an upgrade's image is.
func checkInstallImage(t *target, img *diskImage, signCert string) error {
	if got := img.info.Role(); got != t.node.Role {
		return fmt.Errorf("%s is a node of the role %s; the image is of %s", t.name, t.node.Role, got)
	}
	return checkImage(t.cluster, img, signCert)
}

// sendChunks sends what r holds in chunks. A send that finds the stream closed ends it: the node
// refused the image already, and closing the stream returns why.
func sendChunks(r io.Reader, send func(*nodev1.ImageChunk) error) error {
	buf := make([]byte, chunkSize)
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			if err := send(&nodev1.ImageChunk{Data: buf[:n]}); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				return err
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}
