package chalkd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
)

// configure parses text, YAML config documents, and makes them the config slot of the image t
// describes for the node called name, as translateSlot does.
func configure(name, text string, schemas *schema.Schemas, t config.Target) (slot.Stored, error) {
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		return slot.Stored{}, failed(connect.CodeInvalidArgument, "parse the config: %v", err)
	}
	c, err := translateSlot(name, docs, schemas, t)
	if err != nil {
		return slot.Stored{}, failed(connect.CodeInvalidArgument, "the config: %v", err)
	}
	return c, nil
}

// translateSlot returns what the config slot of the image t describes keeps for docs, the config
// documents as delivered. It checks docs as a set against schemas, fills in their defaults and
// translates them into the identity of the node called name.
//
// The set's version is the hash of the documents as delivered, before the defaults, the same hash
// a client computes from the files it sends.
func translateSlot(name string, docs []config.Document, schemas *schema.Schemas, t config.Target) (slot.Stored, error) {
	version, err := config.Hash(docs)
	if err != nil {
		return slot.Stored{}, fmt.Errorf("hash the config: %w", err)
	}
	set, id, err := config.Translate(name, docs, schemas, t)
	if err != nil {
		return slot.Stored{}, err
	}
	data, err := json.Marshal(id)
	if err != nil {
		return slot.Stored{}, fmt.Errorf("encode the identity: %w", err)
	}
	return slot.Stored{Delivered: docs, Documents: set.Documents(), Version: version, Identity: data}, nil
}

// runningTarget is the running image, as config for it is translated, and its schemas.
func (s *Server) runningTarget() (config.Target, *schema.Schemas, error) {
	schemas, err := schema.LoadSchemas(s.Loader.Schemas)
	if err != nil {
		return config.Target{}, nil, failed(connect.CodeFailedPrecondition, "read the image's schemas: %v", err)
	}
	release := readOSRelease(s.Paths.OSRelease)
	t := config.Target{Platform: release["CHALKOS_PLATFORM"]}
	// The facts the image's layers build their config from at boot, so the config is checked
	// against exactly those; an image whose layers have none carries none. They are the image's
	// own, so a fault in them is not the client's.
	facts, err := imageFacts(layer.Active[layer.ImageFacts](s.Plugins), layer.ImageFacts.RunningFacts)
	if err != nil {
		return config.Target{}, nil, failed(connect.CodeInternal, "the image's layer facts: %v", err)
	}
	if err := s.target(facts, &t); err != nil {
		return config.Target{}, nil, failed(connect.CodeInternal, "the image's layer facts: %v", plain(err))
	}
	return t, schemas, nil
}

// installConfig translates the config an install header delivers into the identity of the node
// for the image installed, and returns the config slot of that image, identity included. In place,
// that image is the running one; from the installer, the header and contract describe it. The
// config meets that image's rules with no running config to compare with: rules of a field that
// read oldSelf do not run, the rules of a kind see no oldDocuments, and every fact is null:
// an install in place that is tried again must not read what the attempt before it left on STATE.
func (s *Server) installConfig(ctx context.Context, h *nodev1.InstallHeader, contract *nodev1.ImageContract) (slot.Stored, error) {
	if h.Config == "" {
		return slot.Stored{}, failed(connect.CodeInvalidArgument, "the header delivers no config")
	}
	name, err := headerName(h)
	if err != nil {
		return slot.Stored{}, err
	}
	var (
		t       config.Target
		schemas *schema.Schemas
	)
	switch {
	case s.Installer:
		t, schemas, err = s.headerTarget(h, contract)
	case contract != nil:
		err = failed(connect.CodeInvalidArgument, "an install in place checks the config against the running image; the header must carry no schemas or image.json")
	default:
		t, schemas, err = s.runningTarget()
	}
	if err != nil {
		return slot.Stored{}, err
	}
	stored, err := configure(name, h.Config, schemas, t)
	if err != nil {
		return slot.Stored{}, err
	}
	compile := compileRunningRules
	if s.Installer {
		compile = compileImageRules
	}
	set, err := compile(schemas)
	if err != nil {
		return slot.Stored{}, err
	}
	f, err := s.factsFor(ctx, stored.Documents, nil, h.FallbackSecret)
	if err != nil {
		return slot.Stored{}, err
	}
	f.Install = true
	if err := s.checkRules(set, name, stored.Documents, nil, f); err != nil {
		return slot.Stored{}, err
	}
	// In place, the running image's renderers check the outputs before the install changes
	// anything; the node renders them again at the boot that follows. The installer cannot run the
	// installed image's, so that image renders its outputs at its first boot alone.
	if !s.Installer {
		staged, err := s.renderOutputs(ctx, name, stored.Documents, schemas, true)
		if err != nil {
			return slot.Stored{}, err
		}
		if err := staged.ClearFailures(); err != nil {
			log.Printf("clear the outputs' failures: %v", err)
		}
		staged.Discard()
	}
	return stored, nil
}

// headerName is the name of the node an install header installs: the subject of the node
// certificate it carries, the node certificate followed by the node CA's, which chalkctl issues
// to the node's name in the inventory. The node has no node certificate of its own yet, so this
// is the only name an install translates its config for; every header carries the certificate,
// as the install needs it to write the node's credentials.
func headerName(h *nodev1.InstallHeader) (string, error) {
	cert, err := pki.ParseCertificate(h.NodeCertificate)
	if err != nil {
		return "", failed(connect.CodeInvalidArgument, "the header's node certificate: %v", err)
	}
	if cert.Subject.CommonName == "" {
		return "", failed(connect.CodeInvalidArgument, "the header's node certificate names no node")
	}
	return cert.Subject.CommonName, nil
}

// headerTarget is the image an install from the installer writes, as its header and contract
// describe it, and the contract's schemas.
func (s *Server) headerTarget(h *nodev1.InstallHeader, contract *nodev1.ImageContract) (config.Target, *schema.Schemas, error) {
	if h.Image == nil {
		return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the installer needs the node's image to write to the target disk")
	}
	if len(contract.GetSchemas()) == 0 {
		return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the header carries config but not the schemas of the image it installs")
	}
	schemas, err := schema.LoadFiles(contract.GetSchemas())
	if err != nil {
		return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the image's schemas: %v", err)
	}
	t := config.Target{Platform: h.Image.GetPlatform()}
	if err := s.target(contract.GetLayerFacts(), &t); err != nil {
		return config.Target{}, nil, err
	}
	return t, schemas, nil
}

// target sets on t what the facts give config's translation, through the plugin each is of;
// facts no compiled plugin claims are refused.
func (s *Server) target(facts []*anypb.Any, t *config.Target) error {
	for _, f := range facts {
		claimed := false
		for _, h := range layer.Compiled[layer.ImageFacts](s.Plugins) {
			ok, err := h.Target(f, t)
			if err != nil {
				return err
			}
			if claimed = ok; ok {
				break
			}
		}
		if !claimed {
			return failed(connect.CodeInvalidArgument, "the image's contract carries facts of type %s, which this chalkd does not know", f.GetTypeUrl())
		}
	}
	return nil
}

// imageFacts collects the facts each plugin reads, nil ones left out.
func imageFacts(hooks []layer.ImageFacts, read func(layer.ImageFacts) (*anypb.Any, error)) ([]*anypb.Any, error) {
	var list []*anypb.Any
	for _, h := range hooks {
		f, err := read(h)
		if err != nil {
			return nil, err
		}
		if f != nil {
			list = append(list, f)
		}
	}
	return list, nil
}

// runningSlot is the running image's config slot on STATE and what it holds; found is false while
// it holds nothing.
func (s *Server) runningSlot() (dir string, stored slot.Stored, found bool, err error) {
	if dir, err = s.Loader.Slot(); err != nil {
		return "", slot.Stored{}, false, failed(connect.CodeInternal, "find the running image's config slot: %v", err)
	}
	if stored, found, err = slot.Read(dir); err != nil {
		return "", slot.Stored{}, false, failed(connect.CodeInternal, "read the running image's config slot: %v", err)
	}
	return dir, stored, found, nil
}

// GetConfig returns the documents a config slot keeps as they were delivered, the running
// image's, its previous one or the staged image's, with every secret value replaced by its hash,
// so chalkctl can show what a delivery changes. It needs the role ApplyConfig needs: names,
// addresses and the shape of the config tell an attacker as much as the values would.
func (s *Server) GetConfig(ctx context.Context, req *connect.Request[nodev1.GetConfigRequest]) (*connect.Response[nodev1.GetConfigResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.Loader.Slot()
	if err != nil {
		return nil, failed(connect.CodeInternal, "find the running image's config slot: %v", err)
	}
	running := dir
	switch {
	case req.Msg.Staged && req.Msg.Previous:
		return nil, failed(connect.CodeInvalidArgument, "ask for the staged image's config or the running image's previous one, not both")
	case req.Msg.Staged:
		staged, err := s.stagedImage()
		if err != nil {
			return nil, failed(connect.CodeInternal, "find the staged image: %v", err)
		}
		if staged == nil {
			return nil, failed(connect.CodeNotFound, "no image is staged to boot next")
		}
		dir = slot.Dir(s.Loader.Slots, staged)
	case req.Msg.Previous:
		dir = slot.Previous(dir)
	}
	stored, found, err := slot.Read(dir)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the config slot: %v", err)
	}
	// A write interrupted between the previous link and the slot's leaves both naming one set,
	// which is no config to roll back to, as Status and RollbackConfig say.
	if found && req.Msg.Previous {
		version, err := slot.Version(running)
		if err != nil {
			return nil, failed(connect.CodeInternal, "read the config's version: %v", err)
		}
		found = stored.Version != "" && stored.Version != version
	}
	switch {
	case !found && req.Msg.Previous:
		return nil, failed(connect.CodeNotFound, "the node keeps no previous config of the running image")
	case !found:
		return nil, failed(connect.CodeNotFound, "STATE holds no config for the image")
	}
	e, err := config.Encode(config.Redact(stored.Delivered))
	if err != nil {
		return nil, failed(connect.CodeInternal, "encode the config: %v", err)
	}
	return connect.NewResponse(&nodev1.GetConfigResponse{Documents: e.Text, Version: stored.Version}), nil
}
