package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/storage"
)

func (s *Server) Install(ctx context.Context, stream *connect.ClientStream[nodev1.InstallRequest]) (*connect.Response[nodev1.InstallResponse], error) {
	if !s.mu.TryLock() {
		return nil, failed(connect.CodeAborted, "another install is running")
	}
	defer s.mu.Unlock()
	if s.installed {
		return nil, failed(connect.CodeFailedPrecondition, "the node is installed and reboots")
	}
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, failed(connect.CodeInvalidArgument, "the install request has no header")
	}
	h := stream.Msg().GetHeader()
	if h == nil {
		return nil, failed(connect.CodeInvalidArgument, "the first install message must be the header")
	}
	var id manifest.Identity
	if err := json.Unmarshal([]byte(h.Identity), &id); err != nil {
		return nil, failed(connect.CodeInvalidArgument, "parse the identity: %v", err)
	}
	req := install.Request{
		Identity:        []byte(h.Identity),
		Section:         id.Storage,
		Kubernetes:      id.Kubernetes,
		NodeCertificate: h.NodeCertificate,
		NodeKey:         h.NodeKey,
		CA:              h.CaCertificate,
		FallbackSecret:  h.FallbackSecret,
		KubernetesShare: h.KubernetesShare,
	}

	var err error
	switch target := h.Target.(type) {
	case *nodev1.InstallHeader_InPlace:
		if s.Installer {
			return nil, failed(connect.CodeFailedPrecondition, "the installer installs nodes onto a disk; name the target disk")
		}
		log.Print("installing in place")
		err = s.InPlace(ctx, req)
	case *nodev1.InstallHeader_Disk:
		if !s.Installer {
			return nil, failed(connect.CodeFailedPrecondition, "the node runs its role image already; install it in place")
		}
		d := target.Disk
		ref := storage.Ref{Path: d.Path, Selector: storage.Selector{Model: d.Model, Serial: d.Serial, WWN: d.Wwn, Size: d.Size, Type: d.Type}}
		log.Printf("installing onto %s", ref)
		err = s.FromMedia(ctx, install.MediaRequest{
			Request: req,
			Target:  ref,
			Image: &chunkReader{next: func() (*nodev1.ImageChunk, bool) {
				if !stream.Receive() {
					return nil, false
				}
				return stream.Msg().GetChunk(), true
			}, err: stream.Err},
			ImageSize:         int64(h.ImageSize),
			ImageSHA256:       h.ImageSha256,
			SystemDefinitions: h.SystemDefinitions,
			WipeDisk:          h.WipeDisk,
		})
	default:
		return nil, failed(connect.CodeInvalidArgument, "the install header names no target")
	}
	if err != nil {
		log.Printf("install failed: %v", err)
		return nil, failed(connect.CodeFailedPrecondition, "install: %v", err)
	}
	s.installed = true
	log.Print("installed; rebooting")
	s.rebootSoon()
	return connect.NewResponse(&nodev1.InstallResponse{}), nil
}

// chunkReader reads an image from the chunks that follow a header: next returns the next message's
// chunk, nil when the message is no chunk, and false at the stream's end, when err says why.
type chunkReader struct {
	next func() (*nodev1.ImageChunk, bool)
	err  func() error
	buf  []byte
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		chunk, ok := r.next()
		if !ok {
			if err := r.err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if chunk == nil {
			return 0, errors.New("a second header follows the image")
		}
		r.buf = chunk.Data
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
