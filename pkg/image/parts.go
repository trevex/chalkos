package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/trevex/chalkos/pkg/uki"
)

// Parts are what a node installs of a disk image: its store and hash tree, and copies of its UKI
// and, when asked for, its boot loader, with what the UKI says of the image.
type Parts struct {
	Binaries
	Store Store
	// Info is what the UKI says of the image.
	Info uki.Image
	raw  *os.File
	dir  string
}

// OpenParts opens the raw image whose partitions parts lists and copies its UKI and, with
// bootLoader, its boot loader into a temporary directory, which Close removes. It refuses an image
// whose hash tree has another root hash than repart reported, or whose UKI boots another store.
func OpenParts(ctx context.Context, raw string, parts []Partition, bootLoader bool) (_ *Parts, err error) {
	esp, err := FindPartition(parts, "esp")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(raw)
	if err != nil {
		return nil, err
	}
	p := &Parts{raw: f}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()
	if p.Store, err = ReadStore(f, parts); err != nil {
		return nil, fmt.Errorf("%s: %w", raw, err)
	}
	if p.dir, err = os.MkdirTemp("", "chalkos-image-"); err != nil {
		return nil, err
	}
	if p.Binaries, err = ExtractBinaries(ctx, raw, esp.Offset, p.dir, bootLoader); err != nil {
		return nil, err
	}
	u, err := os.Open(p.UKI)
	if err != nil {
		return nil, err
	}
	defer u.Close()
	if p.Info, err = uki.Read(u); err != nil {
		return nil, fmt.Errorf("the image's UKI: %w", err)
	}
	if hash, err := p.Info.UsrHash(); err != nil || !bytes.Equal(hash, p.Store.RootHash) {
		return nil, errors.New("the image's UKI boots another store than the image holds")
	}
	return p, nil
}

// Close closes the raw image and removes the copies of the UKI and the boot loader.
func (p *Parts) Close() error {
	err := p.raw.Close()
	if p.dir != "" {
		os.RemoveAll(p.dir)
	}
	return err
}
