package chalkd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"path/filepath"
	"runtime"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Upgrade installs the image an operator streams. It checks that the image is of the node's
// platform, whatever its variant, that it boots the store it carries and, before it becomes the
// next boot, that the schemas in the image accept the node's stored config, but cannot tell an
// image to trust from another: without Secure Boot, an operator can run any image as root, with
// STATE and VAR unsealed, as their keys are sealed to PCR 7 alone. Secure Boot is what limits
// upgrades to images signed for db.
func (s *Server) Upgrade(ctx context.Context, stream *connect.ClientStream[nodev1.UpgradeRequest]) (*connect.Response[nodev1.UpgradeResponse], error) {
	if !s.upgrading.TryLock() {
		return nil, failed(connect.CodeAborted, "another upgrade is running")
	}
	defer s.upgrading.Unlock()
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, failed(connect.CodeInvalidArgument, "the upgrade request has no header")
	}
	h := stream.Msg().GetHeader()
	if h == nil {
		return nil, failed(connect.CodeInvalidArgument, "the first upgrade message must be the header")
	}
	img := h.GetImage()
	switch {
	case img == nil:
		return nil, failed(connect.CodeInvalidArgument, "the header names no image")
	case img.GetBootLoader() != nil:
		return nil, failed(connect.CodeInvalidArgument, "an upgrade carries no boot loader: it leaves the node's as it is")
	}
	header := imageHeader(img)
	log.Printf("upgrading to %q with the root hash %x", img.GetImageId(), img.GetRootHash())
	res, err := s.installImage(ctx, header, &chunkReader{next: func() (*nodev1.ImageChunk, bool) {
		if !stream.Receive() {
			return nil, false
		}
		return stream.Msg().GetChunk(), true
	}, err: stream.Err})
	if err != nil {
		log.Printf("upgrade to %x failed: %v", img.GetRootHash(), err)
		return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v", err)
	}
	s.recordImage(header)
	resp := &nodev1.UpgradeResponse{AlreadyInstalled: res.AlreadyInstalled, AlreadyStaged: res.AlreadyStaged, Entry: res.Entry}
	if res.AlreadyInstalled {
		log.Printf("%x runs already", img.GetRootHash())
		return connect.NewResponse(resp), nil
	}
	if res.AlreadyStaged {
		log.Printf("%x boots next already, as %s", img.GetRootHash(), res.Entry)
	}
	if h.Reboot {
		log.Printf("rebooting into %x", img.GetRootHash())
		resp.Rebooting = true
		s.rebootSoon()
	}
	return connect.NewResponse(resp), nil
}

// Where an image holds the schemas of its kinds and its Kubernetes facts, below its system.
const (
	imageSchemas = "/etc/chalkos/schemas"
	imageFacts   = "/etc/chalkos/kubernetes/image.json"
)

// imageStore is the store of an image an upgrade wrote, read as upgrade.Store reads it: by the
// image's own paths.
type imageStore interface {
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]fs.DirEntry, error)
}

// checkStoredConfig refuses an upgrade to an image that would not accept the node's stored
// config: the node boots the image with that config, which chalkos-config would refuse, and
// boot counting would fall back once the image's tries are used up. It runs once the image's
// store is written to the inactive slot and before the image becomes the next boot, and reads
// the schemas and image.json from that store, whose content the image's root hash covers, so
// the config is checked against exactly what the image will translate it with. system is the
// image's NixOS system in st, and platform is the image's. A node without config
// translates none at boot, whatever the image.
func (s *Server) checkStoredConfig(st imageStore, system, platform string) error {
	// ApplyConfig replaces the stored set under the same lock.
	s.mu.Lock()
	stored, found, err := store.Read(s.Loader.Config)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("read the stored config: %w", err)
	}
	if !found {
		return nil
	}
	entries, err := st.ReadDir(system + imageSchemas)
	if err != nil {
		return fmt.Errorf("the image's schemas: %w", err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if files[e.Name()], err = st.ReadFile(system + imageSchemas + "/" + e.Name()); err != nil {
			return fmt.Errorf("the image's schemas: %w", err)
		}
	}
	schemas, err := schema.LoadFiles(files)
	if err != nil {
		return fmt.Errorf("the image's schemas: %w", err)
	}
	facts, err := st.ReadFile(system + imageFacts)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the image's Kubernetes facts: %w", err)
	}
	t, err := imageTarget(platform, facts)
	if err != nil {
		return err
	}
	if _, _, err := translate(stored.Documents, schemas, t); err != nil {
		return fmt.Errorf("the node's config does not suit the image: %w; apply config both images accept with chalkctl apply-config, then upgrade", err)
	}
	return nil
}

// imageHeader is the image an install or upgrade header describes.
func imageHeader(h *nodev1.ImageHeader) upgrade.Header {
	return upgrade.Header{
		ImageID: h.GetImageId(), Platform: h.GetPlatform(), Architecture: h.GetArchitecture(), RootHash: h.GetRootHash(),
		StoreSize: int64(h.GetStore().GetSize()), VeritySize: int64(h.GetHashTree().GetSize()), UKISize: int64(h.GetUki().GetSize()),
		StoreSHA256: h.GetStore().GetSha256(), VeritySHA256: h.GetHashTree().GetSha256(), UKISHA256: h.GetUki().GetSha256(),
		BootLoaderSize: int64(h.GetBootLoader().GetSize()), BootLoaderSHA256: h.GetBootLoader().GetSha256(),
		Provenance: upgrade.Provenance{Revision: h.GetRevision(), Tag: h.GetTag()},
	}
}

// recordImage keeps the provenance of the image an upgrade installed or found installed in the
// record of installed images, which forgets every image no UKI on the ESP boots any more. The
// record is shown, never relied on, so failing to keep it fails no upgrade.
func (s *Server) recordImage(h upgrade.Header) {
	entries, err := upgrade.Entries(s.Paths.ESP)
	if err != nil {
		log.Printf("record the image's provenance: %v", err)
		return
	}
	var keep [][]byte
	for _, e := range entries {
		if e.RootHash != nil {
			keep = append(keep, e.RootHash)
		}
	}
	path := filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord)
	data, err := upgrade.ReadImages(path).Record(h.RootHash, h.Provenance, keep).Encode()
	if err == nil {
		err = install.WriteFile(path, data, 0o644)
	}
	if err != nil {
		log.Printf("record the image's provenance: %v", err)
	}
}

// installImage installs an upgrade's image on the node's boot disk.
func (s *Server) installImage(ctx context.Context, h upgrade.Header, image io.Reader) (upgrade.Result, error) {
	if s.InstallImage != nil {
		return s.InstallImage(ctx, h, image)
	}
	boot, err := s.Host.ResolvePath(s.Paths.BootDisk)
	if err != nil {
		return upgrade.Result{}, fmt.Errorf("find the disk the node booted from: %w", err)
	}
	n := &upgrade.Node{
		Run:           s.Run,
		Disk:          boot.Device,
		ESP:           s.Paths.ESP,
		EFIVars:       s.Paths.EFIVars,
		Cmdline:       s.Paths.Cmdline,
		OSRelease:     readOSRelease(s.Paths.OSRelease),
		Architecture:  uki.GoArchitecture(runtime.GOARCH),
		OpenPartition: upgrade.OpenPartition,
		// ApplyConfig and ResetVolume change the boot disk's partitions under the same lock.
		TableLock: &s.mu,
		CheckStore: func(st upgrade.Store) error {
			return s.checkStoredConfig(st, st.Toplevel, h.Platform)
		},
	}
	return n.Install(ctx, h, image)
}
