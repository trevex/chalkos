package chalkd

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Upgrade installs the image an operator streams. It checks that the image is of the node's
// role and platform, that the node's stored config suits it and that it boots the store it
// carries, but cannot tell an image to trust from another: without Secure Boot, an operator can
// run any image as root, with STATE and VAR unsealed, as their keys are sealed to PCR 7 alone.
// Secure Boot is what limits upgrades to images signed for db.
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
	if err := s.checkStoredConfig(h); err != nil {
		log.Printf("upgrade to %q refused: %v", img.GetVersion(), err)
		return nil, err
	}
	header := imageHeader(img)
	log.Printf("upgrading to %q %q with the root hash %x", img.GetImageId(), img.GetVersion(), img.GetRootHash())
	res, err := s.installImage(ctx, header, &chunkReader{next: func() (*nodev1.ImageChunk, bool) {
		if !stream.Receive() {
			return nil, false
		}
		return stream.Msg().GetChunk(), true
	}, err: stream.Err})
	if err != nil {
		log.Printf("upgrade to %q failed: %v", img.GetVersion(), err)
		return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v", err)
	}
	resp := &nodev1.UpgradeResponse{AlreadyInstalled: res.AlreadyInstalled, Entry: res.Entry}
	if res.AlreadyInstalled {
		log.Printf("%s runs already", img.GetVersion())
		return connect.NewResponse(resp), nil
	}
	if h.Reboot {
		log.Printf("rebooting into %s", img.GetVersion())
		resp.Rebooting = true
		s.rebootSoon()
	}
	return connect.NewResponse(resp), nil
}

// checkStoredConfig refuses an upgrade to an image that would not accept the node's stored
// config: the node boots the image with that config, which chalkos-identity would refuse, and
// boot counting would fall back once the image's tries are used up. The stored set is checked
// against the schemas and image.json the header carries, as an install from the installer
// checks the config it delivers, before anything is written.
func (s *Server) checkStoredConfig(h *nodev1.UpgradeHeader) error {
	if len(h.Schemas) == 0 {
		return failed(connect.CodeInvalidArgument, "the header carries no schemas of the image, which the node checks its config against; upgrade with the chalkctl of this chalkos")
	}
	t, schemas, err := imageTarget(h.Image, h.Schemas, h.KubernetesImage)
	if err != nil {
		return err
	}
	stored, found, err := store.Read(s.Identity.Config)
	if err != nil {
		return failed(connect.CodeInternal, "read the stored config: %v", err)
	}
	// A node without config translates none at boot, whatever the image.
	if !found {
		return nil
	}
	if _, _, err := translate(stored.Documents, schemas, t); err != nil {
		return failed(connect.CodeFailedPrecondition, "the node's config does not suit the image: %v; apply config both images accept with chalkctl apply-config, then upgrade", err)
	}
	return nil
}

// imageHeader is the image an install or upgrade header describes.
func imageHeader(h *nodev1.ImageHeader) upgrade.Header {
	return upgrade.Header{
		ImageID: h.GetImageId(), Version: h.GetVersion(), Role: h.GetRole(), Platform: h.GetPlatform(), Architecture: h.GetArchitecture(), RootHash: h.GetRootHash(),
		StoreSize: int64(h.GetStore().GetSize()), VeritySize: int64(h.GetHashTree().GetSize()), UKISize: int64(h.GetUki().GetSize()),
		StoreSHA256: h.GetStore().GetSha256(), VeritySHA256: h.GetHashTree().GetSha256(), UKISHA256: h.GetUki().GetSha256(),
		BootLoaderSize: int64(h.GetBootLoader().GetSize()), BootLoaderSHA256: h.GetBootLoader().GetSha256(),
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
	}
	return n.Install(ctx, h, image)
}
