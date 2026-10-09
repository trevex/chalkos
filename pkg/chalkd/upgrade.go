package chalkd

import (
	"context"
	"fmt"
	"io"
	"log"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/upgrade"
)

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
	header := upgrade.Header{
		ImageID: h.ImageId, Version: h.Version, Cluster: h.Cluster, Role: h.Role, RootHash: h.RootHash,
		StoreSize: int64(h.StoreSize), VeritySize: int64(h.VeritySize), UKISize: int64(h.UkiSize),
		StoreSHA256: h.StoreSha256, VeritySHA256: h.VeritySha256, UKISHA256: h.UkiSha256,
	}
	log.Printf("upgrading to %s %s with the root hash %x", h.ImageId, h.Version, h.RootHash)
	res, err := s.installImage(ctx, header, &chunkReader{next: func() (*nodev1.ImageChunk, bool) {
		if !stream.Receive() {
			return nil, false
		}
		return stream.Msg().GetChunk(), true
	}, err: stream.Err})
	if err != nil {
		log.Printf("upgrade to %s failed: %v", h.Version, err)
		return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v", err)
	}
	resp := &nodev1.UpgradeResponse{AlreadyInstalled: res.AlreadyInstalled, Entry: res.Entry}
	if res.AlreadyInstalled {
		log.Printf("%s runs already", h.Version)
		return connect.NewResponse(resp), nil
	}
	if h.Reboot {
		log.Printf("rebooting into %s", h.Version)
		resp.Rebooting = true
		s.rebootSoon()
	}
	return connect.NewResponse(resp), nil
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
		OpenPartition: upgrade.OpenPartition,
	}
	return n.Install(ctx, h, image)
}
