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

// upgradeImage is what an upgrade sends of a disk image: its store, the store's hash tree and its
// UKI, signed when chalkctl was given the key, with what the UKI says of the image.
type upgradeImage struct {
	raw   *os.File
	store image.Store
	// uki is the UKI's file and info what it says.
	uki  string
	info uki.Image
	// header describes the image to the nodes.
	header *nodev1.ImageHeader
	// cleanup removes the UKI's copy.
	cleanup func()
}

// openUpgradeImage reads an image for an upgrade: a raw image with repart-output.json next to
// it, or the directory nix build makes. The UKI is copied off its ESP into a temporary directory
// and signed there with key and cert when they are given.
func openUpgradeImage(ctx context.Context, path, key, cert string) (_ *upgradeImage, err error) {
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
	img := &upgradeImage{raw: f, cleanup: func() {}}
	defer func() {
		if err != nil {
			img.Close()
		}
	}()
	if img.store, err = image.ReadStore(f, parts); err != nil {
		return nil, fmt.Errorf("%s: %w", raw, err)
	}
	dir, err := os.MkdirTemp("", "chalkctl-upgrade-")
	if err != nil {
		return nil, err
	}
	img.cleanup = func() { os.RemoveAll(dir) }
	if img.uki, err = imagesign.ExtractUKI(ctx, raw, esp.Offset, dir); err != nil {
		return nil, err
	}
	if key != "" {
		if err := imagesign.SignFile(ctx, img.uki, key, cert); err != nil {
			return nil, err
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
		return nil, fmt.Errorf("the image's version %q is not one an upgrade installs: 1 to 23 characters of a-z, 0-9, '.', '~', '^' and '-'", img.info.Version())
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

// checkSignature checks the UKI against a db certificate, as firmware with it in db would.
func (img *upgradeImage) checkSignature(certPEM string) error {
	certs, err := pki.ParseBundle(certPEM)
	if err != nil {
		return fmt.Errorf("the db certificate: %w", err)
	}
	f, err := os.Open(img.uki)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := uki.VerifySignature(f, int64(img.header.Uki.Size), uki.Database{Certificates: certs}, uki.Database{}); err != nil {
		return fmt.Errorf("Secure Boot would refuse the image's UKI: %w", err)
	}
	return nil
}

// parts are the image's store, hash tree and UKI as an upgrade streams them.
func (img *upgradeImage) parts() (io.Reader, func(), error) {
	u, err := os.Open(img.uki)
	if err != nil {
		return nil, nil, err
	}
	data, tree := img.store.Data, img.store.HashTree
	r := io.MultiReader(io.NewSectionReader(data, 0, data.Size()), io.NewSectionReader(tree, 0, tree.Size()), u)
	return r, func() { u.Close() }, nil
}

func (img *upgradeImage) Close() {
	img.raw.Close()
	img.cleanup()
}
