package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Install installs the node, in place or, from the installer, onto a disk with the image that
// follows the header in chunks. An image the header names in a registry is pulled by the digest it
// names, with the credentials of the delivered config's Registry of its host, and checked against
// the header as a streamed one; its contract layer gives the repart definitions, schemas and
// layers' facts.
func (s *Server) Install(ctx context.Context, stream *connect.ClientStream[nodev1.InstallRequest]) (*connect.Response[nodev1.InstallResponse], error) {
	if !s.mu.TryLock() {
		return nil, layer.Errorf(connect.CodeAborted, "another install is running")
	}
	defer s.mu.Unlock()
	if s.installed {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "the node is installed and reboots")
	}
	h, err := readHeader(stream, (*nodev1.InstallRequest).GetHeader, "install")
	if err != nil {
		return nil, err
	}
	// The target must fit the mode before the config is translated for the image it names.
	switch h.Target.(type) {
	case *nodev1.InstallHeader_InPlace:
		if s.Installer {
			return nil, layer.Errorf(connect.CodeFailedPrecondition, "the installer installs nodes onto a disk; name the target disk")
		}
		if h.Image != nil || h.GetSource() != nil {
			return nil, layer.Errorf(connect.CodeInvalidArgument, "an install in place takes no image: the node runs its image already")
		}
	case *nodev1.InstallHeader_Disk:
		if !s.Installer {
			return nil, layer.Errorf(connect.CodeFailedPrecondition, "the node runs its image already; install it in place")
		}
		if h.Image == nil {
			return nil, layer.Errorf(connect.CodeInvalidArgument, "the installer needs the node's image to write to the target disk")
		}
	default:
		return nil, layer.Errorf(connect.CodeInvalidArgument, "the install header names no target")
	}
	if h.Config == "" {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "the header delivers no config")
	}
	// An image the installer pulls brings the contract the config is translated with.
	contract := h.GetContract()
	var pull *ociimage.Pull
	if h.GetSource() != nil {
		var err error
		if pull, contract, err = s.pullInstall(ctx, stream, h); err != nil {
			log.Printf("install failed: %v", err)
			return nil, err
		}
		defer pull.Close()
	}
	name, err := headerName(h)
	if err != nil {
		return nil, err
	}
	stored, err := s.installConfig(ctx, h, name, contract)
	if err != nil {
		return nil, err
	}
	var id identity.Identity
	if err := json.Unmarshal(stored.Identity, &id); err != nil {
		return nil, fmt.Errorf("parse the identity: %w", err)
	}
	req := install.Request{
		Section:         id.Storage,
		NodeCertificate: h.NodeCertificate,
		NodeKey:         h.NodeKey,
		CA:              h.CaCertificate,
		FallbackSecret:  h.FallbackSecret,
	}
	switch target := h.Target.(type) {
	case *nodev1.InstallHeader_InPlace:
		running, rerr := uki.RunningHash(s.Paths.Cmdline)
		if rerr != nil {
			return nil, rerr
		}
		req.Config = stored
		if req.Layers, err = s.installPayloads(ctx, layer.Active[layer.Delivery](s.Plugins), h, stored.Identity); err != nil {
			return nil, err
		}
		log.Print("installing in place")
		err = s.InPlace(ctx, req, running)
	case *nodev1.InstallHeader_Disk:
		d := target.Disk
		ref := storage.Ref{Path: d.Path, Selector: storage.Selector{Model: d.Model, Serial: d.Serial, WWN: d.Wwn, Size: d.Size, Type: d.Type}}
		// The payloads are planned against the installer's own translation before the disk
		// changes, so one no layer takes, or a share for another node or kind, is refused with the
		// disk untouched. installCheck plans them again against the identity the image's checker
		// gives, which STATE records them with.
		if _, err := s.installPayloads(ctx, layer.Compiled[layer.Delivery](s.Plugins), h, stored.Identity); err != nil {
			return nil, err
		}
		img := imageHeader(h.Image)
		parts := chunks(stream)
		from := ""
		if pull != nil {
			parts, from = pull, ", pulled from "+h.GetSource().GetOci().GetReference()
		}
		log.Printf("installing %q with the root hash %x onto %s%s", img.ImageID, img.RootHash, ref, from)
		// The node starts its record of installed images with this one.
		if req.Images, err = (upgrade.Images{}).Record(img.RootHash, img.Provenance, nil).Encode(); err != nil {
			return nil, fmt.Errorf("the record of the image's provenance: %w", err)
		}
		err = s.FromParts(ctx, install.PartsRequest{
			Request:           req,
			Target:            ref,
			Image:             img,
			Parts:             parts,
			SystemDefinitions: contract.GetSystemDefinitions(),
			WipeDisk:          h.WipeDisk,
			Check: func(ctx context.Context, st install.Store, system string) (install.Checked, error) {
				return s.installCheck(ctx, st, system, h, name, stored.Delivered)
			},
		})
	}
	if err != nil {
		log.Printf("install failed: %v", err)
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "install: %w", err)
	}
	s.installed = true
	log.Print("installed; rebooting")
	s.rebootSoon()
	return connect.NewResponse(&nodev1.InstallResponse{}), nil
}

// installCheck has the image an install from the installer writes, in the store st of the
// target's slot A with the NixOS system system, check docs for the node name with its own checker,
// against no running config. The installer's machine has no STATE or volumes, so facts of what
// the node holds read null; its hardware is the node's. It returns the image's config slot, Secret
// values restored, and the header's payloads, checked against the checker's identity by the
// layers compiled into the installer, which has no layer of its own. Install holds s.mu.
func (s *Server) installCheck(ctx context.Context, st imageStore, system string, h *nodev1.InstallHeader, name string, docs []config.Document) (install.Checked, error) {
	root := h.GetImage().GetRootHash()
	next, warnings, err := s.checkWithImage(ctx, st, system, h.GetImage().GetPlatform(), name, imageCheck{delivered: docs, secret: h.FallbackSecret, image: root, writing: root})
	if err != nil {
		return install.Checked{}, err
	}
	logWarnings(root, warnings)
	payloads, err := s.installPayloads(ctx, layer.Compiled[layer.Delivery](s.Plugins), h, next.Identity)
	if err != nil {
		return install.Checked{}, err
	}
	return install.Checked{Config: next, Layers: payloads}, nil
}

// installPayloads hands the header's payloads to the layers among deliveries that take them,
// planned as an install with the identity the node is installed with, and returns their writers.
func (s *Server) installPayloads(ctx context.Context, deliveries []layer.Delivery, h *nodev1.InstallHeader, identity []byte) ([]func(stateDir string) error, error) {
	commits, err := layer.Plan(ctx, deliveries, h.Layers, layer.Planned{Identity: identity, OSCA: string(h.CaCertificate), Install: true})
	if err != nil {
		return nil, err
	}
	var writers []func(stateDir string) error
	for _, c := range commits {
		if c.Payloads != nil {
			writers = append(writers, c.Payloads)
		}
	}
	return writers, nil
}

// pullInstall prepares the pull of the image an install from the installer names in a registry,
// whose chunks the client does not send. It authenticates with the Registry of the reference's
// host in the header's config, the only config the installer has. Before anything changes it
// fetches the manifest, header and contract layer, and returns the contract, which the manifest's
// digest pins. Install logs each refusal it returns.
func (s *Server) pullInstall(ctx context.Context, stream *connect.ClientStream[nodev1.InstallRequest], h *nodev1.InstallHeader) (*ociimage.Pull, *nodev1.ImageContract, error) {
	ref := h.GetSource().GetOci().GetReference()
	img := imageHeader(h.Image)
	if ref == "" {
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "the header's source names no image in a registry")
	}
	if err := img.Validate(); err != nil {
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "the image: %w", err)
	}
	if img.BootLoader.Size == 0 {
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "the image brings no boot loader, which an install writes to the ESP")
	}
	if err := noChunks(stream, "an install"); err != nil {
		return nil, nil, err
	}
	docs, err := config.Parse(strings.NewReader(h.Config))
	if err != nil {
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "install: pull %s: parse the config: %w", ref, err)
	}
	pull, err := newPull(ctx, ref, img, docs)
	if err != nil {
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "install: %w", err)
	}
	c, err := pull.Contract()
	if err != nil {
		pull.Close()
		return nil, nil, layer.Errorf(connect.CodeFailedPrecondition, "install: %w", err)
	}
	contract := &nodev1.ImageContract{Schemas: c.Schemas, SystemDefinitions: make(map[string]string, len(c.Definitions))}
	// The pulled image is not the one that runs, so every compiled plugin reads its facts.
	if contract.LayerFacts, err = imageFacts(layer.Compiled[layer.ImageFacts](s.Plugins), func(h layer.ImageFacts) (*anypb.Any, error) {
		return h.ContractFacts(c.ReadFile)
	}); err != nil {
		pull.Close()
		return nil, nil, fmt.Errorf("install: %w", err)
	}
	for name, data := range c.Definitions {
		contract.SystemDefinitions[name] = string(data)
	}
	return pull, contract, nil
}

// chunkMessage is a request that carries a chunk of an image after the header of its stream.
type chunkMessage interface {
	GetChunk() *nodev1.ImageChunk
}

// chunks reads the image the requests after the header of stream carry.
func chunks[T any, M interface {
	*T
	chunkMessage
}](stream *connect.ClientStream[T]) io.Reader {
	return &chunkReader{next: func() (*nodev1.ImageChunk, bool) {
		if !stream.Receive() {
			return nil, false
		}
		return M(stream.Msg()).GetChunk(), true
	}, err: stream.Err}
}

// readHeader reads the first message of an install or upgrade (what), which must carry its header.
func readHeader[T any, H comparable](stream *connect.ClientStream[T], get func(*T) H, what string) (H, error) {
	var none H
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return none, err
		}
		return none, layer.Errorf(connect.CodeInvalidArgument, "the %s request has no header", what)
	}
	h := get(stream.Msg())
	if h == none {
		return none, layer.Errorf(connect.CodeInvalidArgument, "the first %s message must be the header", what)
	}
	return h, nil
}

// logWarnings logs the warnings of the check of the config of the image with the root hash: the
// answers of an install and an upgrade have none, and the journal keeps them.
func logWarnings(rootHash []byte, warnings []string) {
	for _, w := range warnings {
		log.Printf("the config of %x: %s", rootHash, w)
	}
}

// noChunks refuses any request after the header: an install or upgrade (what) that pulls its
// image from a registry gets no chunks from the client. Receive returns once the client closed its
// side.
func noChunks[T any](stream *connect.ClientStream[T], what string) error {
	if stream.Receive() {
		return layer.Errorf(connect.CodeInvalidArgument, "%s that pulls its image carries no chunks", what)
	}
	return stream.Err()
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
