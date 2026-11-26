package chalkd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
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
// layers' facts. In place, the node installs the image it runs or one it pulls, which goes into
// the inactive slot as an upgrade's does and boots once the node is installed; the pulled image's
// own checker checks the config.
func (s *Server) Install(ctx context.Context, stream nodev1connect.NodeServiceInstallServerStream) (*nodev1.InstallResponse, error) {
	return s.install(ctx, boundIdle(ctx, stream, s.idleBound()))
}

// install is Install of the requests of stream. It reads and checks the header before it takes
// s.mu, so a client that sends nothing holds no lock.
func (s *Server) install(ctx context.Context, stream requests[*nodev1.InstallRequest]) (*nodev1.InstallResponse, error) {
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
		// The node installs in place the image it runs, or one it pulls from a registry.
		if h.Image != nil && h.GetSource() == nil {
			return nil, layer.Errorf(connect.CodeInvalidArgument, "an install in place takes no streamed image: name the image in a registry, which the node pulls")
		}
		if h.GetSource() != nil && h.Image == nil {
			return nil, layer.Errorf(connect.CodeInvalidArgument, "the header describes no image of the source it names")
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
	if !s.mu.TryLock() {
		return nil, layer.Errorf(connect.CodeAborted, "another install is running")
	}
	defer s.mu.Unlock()
	defer s.muBy.hold("an install")()
	if s.installed {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "the node is installed and reboots")
	}
	// An image the node pulls brings the contract the config is translated with.
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
	// An image pulled to install in place goes into the inactive slot and checks the config there,
	// unless the node runs it already; then the running image checks the config as without one.
	var running []byte
	placing := false
	if _, ok := h.Target.(*nodev1.InstallHeader_InPlace); ok {
		if running, err = uki.RunningHash(s.Paths.Cmdline); err != nil {
			return nil, err
		}
		placing = pull != nil && !bytes.Equal(h.Image.GetRootHash(), running)
		if pull != nil && !placing {
			// The running image checks the config against its own contract; the header carries
			// none, as its origin is the source.
			contract = nil
		}
	}
	name, err := headerName(h)
	if err != nil {
		return nil, err
	}
	stored, err := s.installConfig(ctx, h, name, contract, s.Installer || placing)
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
		if placing {
			// The payloads are planned against chalkd's own translation before anything changes,
			// as from the installer.
			if _, err := s.installPayloads(ctx, layer.Compiled[layer.Delivery](s.Plugins), h, stored.Identity); err != nil {
				return nil, err
			}
			log.Printf("installing in place %q with the root hash %x, pulled from %s", h.Image.GetImageId(), h.Image.GetRootHash(), h.GetSource().GetOci().GetReference())
			err = s.placeInPlace(ctx, h, name, stored, req, pull)
			break
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
		return nil, refused("install", err)
	}
	s.installed = true
	log.Print("installed; rebooting")
	s.rebootSoon()
	return &nodev1.InstallResponse{}, nil
}

// installCheck has the image an install writes, in the store st with the NixOS system system,
// check docs for the node name with its own checker, against no running config: the image the
// installer writes in the target's slot A, or the image an install in place pulled into the
// inactive slot. The installer's machine has no STATE or volumes, so facts of what the node holds
// read null; its hardware is the node's. In place every fact reads null, as the running image
// reads them there. It returns the image's config slot, Secret values restored, and the header's
// payloads, checked against the checker's identity by the layers compiled into chalkd, as the
// image's layers need not run. Install holds s.mu.
func (s *Server) installCheck(ctx context.Context, st imageStore, system string, h *nodev1.InstallHeader, name string, docs []config.Document) (install.Checked, error) {
	root := h.GetImage().GetRootHash()
	next, warnings, err := s.checkWithImage(ctx, st, system, h.GetImage().GetPlatform(), name, imageCheck{delivered: docs, secret: h.FallbackSecret, image: root, writing: root, install: !s.Installer})
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

// placeInPlace installs the node in place with the image pull, which the node does not run, with
// req, which holds the header's credentials. stored is chalkd's own translation of the config, of
// the pulled image's schemas, which InPlace checks the request with before anything changes; the
// pulled image's checker decides. Once those checks passed, the image goes into the inactive slot
// through the slot writer of an upgrade and is staged there once installCheck passed the config
// as delivered on its store. STATE then gets the storage, the config slot and the payloads that
// check gave, and a provenance record of the image, and the image is activated before the node
// is marked installed. An install that failed runs again: the slot writer passes over what it
// wrote, and an image it staged is checked again. Install holds s.mu.
func (s *Server) placeInPlace(ctx context.Context, h *nodev1.InstallHeader, name string, stored slot.Stored, req install.Request, pull *ociimage.Pull) error {
	img := imageHeader(h.Image)
	slots := s.imageSlots()
	req.Config = stored
	var err error
	if req.Images, err = (upgrade.Images{}).Record(img.RootHash, img.Provenance, nil).Encode(); err != nil {
		return fmt.Errorf("the record of the image's provenance: %w", err)
	}
	req.Place = func(ctx context.Context) (install.Checked, error) {
		var checked install.Checked
		res, err := slots.Place(ctx, img, func(st imageStore, system string) error {
			var err error
			checked, err = s.installCheck(ctx, st, system, h, name, stored.Delivered)
			return err
		}, pull)
		if err != nil {
			return install.Checked{}, err
		}
		// Only the node's known-good image is staged without a check, which an install in place
		// never stages.
		if len(checked.Config.Identity) == 0 {
			return install.Checked{}, fmt.Errorf("the image %x staged as %s did not check the config, so the install cannot go on: an upgrade or a rollback must replace the image in the inactive slot first", img.RootHash, res.Entry)
		}
		log.Printf("%x is staged as %s", img.RootHash, res.Entry)
		return checked, nil
	}
	req.ActivateImage = func(ctx context.Context) error {
		res, err := slots.ActivatePlaced(ctx, img.RootHash)
		if err != nil {
			return fmt.Errorf("activate %x: %w", img.RootHash, err)
		}
		log.Printf("%x boots next as %s", img.RootHash, res.Entry)
		return nil
	}
	return s.InPlace(ctx, req, img.RootHash)
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

// pullInstall prepares the pull of the image an install names in a registry, whose chunks the
// client does not send. It authenticates with the Registry of the reference's host in the
// header's config, the only config a node in maintenance mode has. Before anything changes it
// fetches the manifest, header and contract layer, and returns the contract, which the manifest's
// digest pins. The installer writes the image's boot loader to the target's ESP; an install in
// place keeps the node's, so its header names none. Install logs each refusal it returns.
func (s *Server) pullInstall(ctx context.Context, stream requests[*nodev1.InstallRequest], h *nodev1.InstallHeader) (*ociimage.Pull, *nodev1.ImageContract, error) {
	ref := h.GetSource().GetOci().GetReference()
	img := imageHeader(h.Image)
	if ref == "" {
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "the header's source names no image in a registry")
	}
	if err := img.Validate(); err != nil {
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "the image: %w", err)
	}
	switch {
	case s.Installer && img.BootLoader.Size == 0:
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "the image brings no boot loader, which an install writes to the ESP")
	case !s.Installer && img.BootLoader.Size != 0:
		return nil, nil, layer.Errorf(connect.CodeInvalidArgument, "an install in place keeps the node's boot loader: the header must describe none")
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

// requests are the requests of an install's or an upgrade's stream.
type requests[M any] interface {
	// Receive returns the next request, or an error matching io.EOF once the client closed its
	// side.
	Receive() (M, error)
}

// idleStream is how long the client of an install or an upgrade may send nothing before chalkd
// ends the call. ReadHeaderTimeout and IdleTimeout do not bound an open HTTP/2 stream.
const idleStream = 2 * time.Minute

// idleBound is s's bound on how long the client of an install or an upgrade may send nothing.
func (s *Server) idleBound() time.Duration { return cmp.Or(s.streamIdle, idleStream) }

// boundIdle ends the reads of stream, the requests of the call of ctx, once its client sent
// nothing for idle. Each Receive sets the request's read deadline that far ahead, which ends a
// read of an HTTP/2 stream as of an HTTP/1 connection, and clears it once it returned: a deadline
// that passes between reads ends an HTTP/2 stream's next read too.
func boundIdle[M any](ctx context.Context, stream requests[M], idle time.Duration) requests[M] {
	rc, ok := ctx.Value(responseKey{}).(*http.ResponseController)
	if !ok {
		return stream
	}
	return idleRequests[M]{stream: stream, rc: rc, idle: idle}
}

// idleRequests are requests whose reads boundIdle bounds.
type idleRequests[M any] struct {
	stream requests[M]
	rc     *http.ResponseController
	idle   time.Duration
}

func (r idleRequests[M]) Receive() (M, error) {
	// A connection that cannot take deadlines leaves the read unbounded.
	_ = r.rc.SetReadDeadline(time.Now().Add(r.idle))
	msg, err := r.stream.Receive()
	// The call may run on for long before it reads again, or without reading at all.
	_ = r.rc.SetReadDeadline(time.Time{})
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return msg, layer.Errorf(connect.CodeDeadlineExceeded, "the client sent nothing for %v", r.idle)
	}
	return msg, err
}

// refused is the error of an install (what) that failed with err: FAILED_PRECONDITION, unless the
// client's idle bound ended it, which keeps DEADLINE_EXCEEDED.
func refused(what string, err error) error {
	if layer.CodeOf(err) == connect.CodeDeadlineExceeded {
		return fmt.Errorf("%s: %w", what, err)
	}
	return layer.Errorf(connect.CodeFailedPrecondition, "%s: %w", what, err)
}

// chunks reads the image the requests after the header of stream carry.
func chunks[M chunkMessage](stream requests[M]) io.Reader {
	return &chunkReader{next: func() (*nodev1.ImageChunk, error) {
		msg, err := stream.Receive()
		if err != nil {
			return nil, err
		}
		return msg.GetChunk(), nil
	}}
}

// readHeader reads the first message of an install or upgrade (what), which must carry its header.
func readHeader[M any, H comparable](stream requests[M], get func(M) H, what string) (H, error) {
	var none H
	msg, err := stream.Receive()
	if errors.Is(err, io.EOF) {
		return none, layer.Errorf(connect.CodeInvalidArgument, "the %s request has no header", what)
	}
	if err != nil {
		return none, err
	}
	h := get(msg)
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
func noChunks[M any](stream requests[M], what string) error {
	_, err := stream.Receive()
	switch {
	case err == nil:
		return layer.Errorf(connect.CodeInvalidArgument, "%s that pulls its image carries no chunks", what)
	case errors.Is(err, io.EOF):
		return nil
	}
	return err
}

// chunkReader reads an image from the chunks that follow a header: next returns the next message's
// chunk, nil when the message is no chunk, and an error matching io.EOF at the stream's end.
type chunkReader struct {
	next func() (*nodev1.ImageChunk, error)
	buf  []byte
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		chunk, err := r.next()
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
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
