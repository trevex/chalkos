package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Install installs the node, in place or, from the installer, onto a disk with the image that
// follows the header in chunks. An image the header names in a registry is pulled by the digest it
// names, with the credentials of the delivered config's Registry of its host, and checked against
// the header as a streamed one; its contract layer gives the repart definitions, schemas and
// image.json.
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
	// The target must fit the mode before the config is translated for the image it names.
	switch h.Target.(type) {
	case *nodev1.InstallHeader_InPlace:
		if s.Installer {
			return nil, failed(connect.CodeFailedPrecondition, "the installer installs nodes onto a disk; name the target disk")
		}
		if h.Image != nil || h.Source != nil {
			return nil, failed(connect.CodeInvalidArgument, "an install in place takes no image: the node runs its image already")
		}
	case *nodev1.InstallHeader_Disk:
		if !s.Installer {
			return nil, failed(connect.CodeFailedPrecondition, "the node runs its image already; install it in place")
		}
		if h.Image == nil {
			return nil, failed(connect.CodeInvalidArgument, "the installer needs the node's image to write to the target disk")
		}
	default:
		return nil, failed(connect.CodeInvalidArgument, "the install header names no target")
	}
	// An image the installer pulls brings the contract the config is translated with.
	var pull *ociimage.Pull
	if h.Source != nil {
		var err error
		if pull, err = s.pullInstall(ctx, stream, h); err != nil {
			log.Printf("install failed: %v", plain(err))
			return nil, err
		}
		defer pull.Close()
	}
	stored, err := s.installConfig(ctx, h)
	if err != nil {
		return nil, err
	}
	var id identity.Identity
	if err := json.Unmarshal(stored.Identity, &id); err != nil {
		return nil, failed(connect.CodeInternal, "parse the identity: %v", err)
	}
	req := install.Request{
		Config:          stored,
		Section:         id.Storage,
		Kubernetes:      id.Kubernetes,
		NodeCertificate: h.NodeCertificate,
		NodeKey:         h.NodeKey,
		CA:              h.CaCertificate,
		FallbackSecret:  h.FallbackSecret,
		KubernetesShare: h.KubernetesShare,
	}
	switch target := h.Target.(type) {
	case *nodev1.InstallHeader_InPlace:
		running, rerr := s.runningHash()
		if rerr != nil {
			return nil, failed(connect.CodeInternal, "the running image: %v", rerr)
		}
		log.Print("installing in place")
		err = s.InPlace(ctx, req, running)
	case *nodev1.InstallHeader_Disk:
		d := target.Disk
		ref := storage.Ref{Path: d.Path, Selector: storage.Selector{Model: d.Model, Serial: d.Serial, WWN: d.Wwn, Size: d.Size, Type: d.Type}}
		img := imageHeader(h.Image)
		var parts io.Reader = &chunkReader{next: func() (*nodev1.ImageChunk, bool) {
			if !stream.Receive() {
				return nil, false
			}
			return stream.Msg().GetChunk(), true
		}, err: stream.Err}
		from := ""
		if pull != nil {
			parts, from = pull, ", pulled from "+h.Source.GetOci().GetReference()
		}
		log.Printf("installing %q with the root hash %x onto %s%s", img.ImageID, img.RootHash, ref, from)
		// The node starts its record of installed images with this one.
		if req.Images, err = (upgrade.Images{}).Record(img.RootHash, img.Provenance, nil).Encode(); err != nil {
			return nil, failed(connect.CodeInternal, "the record of the image's provenance: %v", err)
		}
		err = s.FromParts(ctx, install.PartsRequest{
			Request:           req,
			Target:            ref,
			Image:             img,
			Parts:             parts,
			SystemDefinitions: h.SystemDefinitions,
			WipeDisk:          h.WipeDisk,
		})
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

// pullInstall prepares the pull of the image an install from the installer names in a registry.
// The client sends no chunks of that image. chalkd authenticates with the Registry of the
// reference's host among the documents of the config the header delivers, the only config the
// installer has. It checks the header's image first, then fetches the manifest, the header and
// the contract layer before the install changes anything, and fills the header's schemas,
// image.json and repart definitions from that contract layer, which the manifest's digest pins;
// the header must carry none of its own. Install logs each refusal as it returns it.
func (s *Server) pullInstall(ctx context.Context, stream *connect.ClientStream[nodev1.InstallRequest], h *nodev1.InstallHeader) (*ociimage.Pull, error) {
	ref := h.GetSource().GetOci().GetReference()
	img := imageHeader(h.Image)
	if ref == "" {
		return nil, failed(connect.CodeInvalidArgument, "the header's source names no image in a registry")
	}
	if err := img.Validate(); err != nil {
		return nil, failed(connect.CodeInvalidArgument, "the image: %v", err)
	}
	switch {
	case img.BootLoaderSize == 0:
		return nil, failed(connect.CodeInvalidArgument, "the image brings no boot loader, which an install writes to the ESP")
	case len(h.Schemas) > 0 || len(h.KubernetesImage) > 0 || len(h.SystemDefinitions) > 0:
		return nil, failed(connect.CodeInvalidArgument, "an install that pulls its image takes the schemas, image.json and repart definitions from the image's contract layer; the header must carry none")
	case h.Config == "":
		return nil, failed(connect.CodeInvalidArgument, "the header delivers no config")
	}
	// The client sends nothing after the header, and Receive returns once it closed its side.
	if stream.Receive() {
		return nil, failed(connect.CodeInvalidArgument, "an install that pulls its image carries no chunks")
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	docs, err := config.Parse(strings.NewReader(h.Config))
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "install: pull %s: parse the config: %v", ref, err)
	}
	pull, err := newPull(ctx, ref, img, docs)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "install: %v", err)
	}
	c, err := pull.Contract()
	if err != nil {
		pull.Close()
		return nil, failed(connect.CodeFailedPrecondition, "install: %v", err)
	}
	h.Schemas, h.KubernetesImage = c.Schemas, c.ImageJSON
	h.SystemDefinitions = make(map[string]string, len(c.Definitions))
	for name, data := range c.Definitions {
		h.SystemDefinitions[name] = string(data)
	}
	return pull, nil
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
