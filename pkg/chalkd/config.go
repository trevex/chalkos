package chalkd

import (
	"context"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/check"
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
		return slot.Stored{}, layer.Errorf(connect.CodeInvalidArgument, "parse the config: %w", err)
	}
	c, err := translateSlot(name, docs, schemas, t)
	if err != nil {
		return slot.Stored{}, layer.Errorf(connect.CodeInvalidArgument, "%s", check.ConfigRefusal(err))
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
	tr, err := check.Translate(name, docs, schemas, t)
	if err != nil {
		return slot.Stored{}, err
	}
	return slot.Stored{Delivered: docs, Documents: tr.Documents, Version: version, Identity: tr.Identity}, nil
}

// runningSchemas are the running image's schemas.
func (s *Server) runningSchemas() (*schema.Schemas, error) {
	schemas, err := schema.LoadSchemas(s.Loader.Schemas)
	if err != nil {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "read the image's schemas: %w", err)
	}
	return schemas, nil
}

// runningTarget is the running image, as config for it is translated, and its schemas.
func (s *Server) runningTarget() (config.Target, *schema.Schemas, error) {
	schemas, err := s.runningSchemas()
	if err != nil {
		return config.Target{}, nil, err
	}
	release := readOSRelease(s.Paths.OSRelease)
	t := config.Target{Platform: release["CHALKOS_PLATFORM"]}
	// The facts the image's layers build their config from at boot, so the config is checked
	// against exactly those; an image whose layers have none carries none. They are the image's
	// own, so a fault in them is not the client's.
	facts, err := imageFacts(layer.Active[layer.ImageFacts](s.Plugins), layer.ImageFacts.RunningFacts)
	if err != nil {
		return config.Target{}, nil, fmt.Errorf("the image's layer facts: %w", err)
	}
	if err := s.target(facts, &t); err != nil {
		return config.Target{}, nil, layer.Errorf(connect.CodeInternal, "the image's layer facts: %w", err)
	}
	return t, schemas, nil
}

// installConfig translates the config an install header delivers, for the node name, into the
// identity of the node for the image installed, and returns the config slot of that image,
// identity included.
//
// In place, that image is the running one, which boots once the node is installed, so it checks
// the config as it checks a delivery, before the install changes anything: its rules, with no
// running config to compare with, so rules of a field that read oldSelf do not run, the rules of a
// kind see no oldDocuments, and every fact is null, as an install in place that is tried again
// must not read what the attempt before it left on STATE; then its validators and its renderers.
//
// From the installer, the header and contract describe the image, and the installer translates
// the config with its own code alone: the storage section of that translation is what it lays the
// target disk out by before the image's store is written. The image's own checker checks the
// config once the store is (installCheck), and its slot is what the install writes.
func (s *Server) installConfig(ctx context.Context, h *nodev1.InstallHeader, name string, contract *nodev1.ImageContract) (slot.Stored, error) {
	var (
		t       config.Target
		schemas *schema.Schemas
		err     error
	)
	switch {
	case s.Installer:
		t, schemas, err = s.headerTarget(h, contract)
	case contract != nil:
		err = layer.Errorf(connect.CodeInvalidArgument, "an install in place checks the config against the running image; the header must carry no schemas or image.json")
	default:
		t, schemas, err = s.runningTarget()
	}
	if err != nil {
		return slot.Stored{}, err
	}
	stored, err := configure(name, h.Config, schemas, t)
	if err != nil || s.Installer {
		return stored, err
	}
	set, err := compileRunningRules(schemas)
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
	// The node renders the outputs again at the boot that follows.
	if err := s.checkValidators(ctx, stored.Documents); err != nil {
		return slot.Stored{}, err
	}
	staged, err := s.renderOutputs(ctx, name, stored.Documents, schemas, true, false)
	if err != nil {
		return slot.Stored{}, err
	}
	if err := staged.ClearFailures(); err != nil {
		log.Printf("clear the outputs' failures: %v", err)
	}
	staged.Discard()
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
		return "", layer.Errorf(connect.CodeInvalidArgument, "the header's node certificate: %w", err)
	}
	if cert.Subject.CommonName == "" {
		return "", layer.Errorf(connect.CodeInvalidArgument, "the header's node certificate names no node")
	}
	return cert.Subject.CommonName, nil
}

// headerTarget is the image an install from the installer writes, as its header and contract
// describe it, and the contract's schemas: what the installer translates the config with to lay
// the target disk out.
func (s *Server) headerTarget(h *nodev1.InstallHeader, contract *nodev1.ImageContract) (config.Target, *schema.Schemas, error) {
	if h.Image == nil {
		return config.Target{}, nil, layer.Errorf(connect.CodeInvalidArgument, "the installer needs the node's image to write to the target disk")
	}
	if len(contract.GetSchemas()) == 0 {
		return config.Target{}, nil, layer.Errorf(connect.CodeInvalidArgument, "the header carries config but not the schemas of the image it installs")
	}
	schemas, err := schema.LoadFiles(contract.GetSchemas())
	if err != nil {
		return config.Target{}, nil, layer.Errorf(connect.CodeInvalidArgument, "the image's schemas: %w", err)
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
			return layer.Errorf(connect.CodeInvalidArgument, "the image's contract carries facts of type %s, which this chalkd does not know", f.GetTypeUrl())
		}
	}
	return nil
}

// imageFacts collects the facts each plugin reads, nil ones left out. The facts are the image's own,
// so a plugin that fails to read them is a failure of the node, whatever code it gives.
func imageFacts(hooks []layer.ImageFacts, read func(layer.ImageFacts) (*anypb.Any, error)) ([]*anypb.Any, error) {
	var list []*anypb.Any
	for _, h := range hooks {
		f, err := read(h)
		if err != nil {
			return nil, layer.Errorf(connect.CodeInternal, "%w", err)
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
		return "", slot.Stored{}, false, fmt.Errorf("find the running image's config slot: %w", err)
	}
	if stored, found, err = slot.Read(dir); err != nil {
		return "", slot.Stored{}, false, fmt.Errorf("read the running image's config slot: %w", err)
	}
	return dir, stored, found, nil
}

// GetConfig returns the documents a config slot keeps as they were delivered, the running
// image's, its previous one, the staged image's or without one the pending config, with every
// secret value replaced by its hash, so chalkctl can show what a delivery changes. It needs the
// role ApplyConfig needs: names, addresses and the shape of the config tell an attacker as much as
// the values would.
func (s *Server) GetConfig(ctx context.Context, req *connect.Request[nodev1.GetConfigRequest]) (*connect.Response[nodev1.GetConfigResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.Loader.Slot()
	if err != nil {
		return nil, fmt.Errorf("find the running image's config slot: %w", err)
	}
	running, pending := dir, false
	switch {
	case req.Msg.Staged && req.Msg.Previous:
		return nil, layer.Errorf(connect.CodeInvalidArgument, "ask for the staged image's config or the running image's previous one, not both")
	case req.Msg.Staged:
		staged, err := s.stagedImage()
		if err != nil {
			return nil, fmt.Errorf("find the staged image: %w", err)
		}
		// Without a staged image, the next image is the one the next upgrade installs, which
		// boots with the pending config when there is one.
		dir, pending = slot.Pending(s.Loader.Slots), staged == nil
		if staged != nil {
			dir = slot.Dir(s.Loader.Slots, staged)
		}
	case req.Msg.Previous:
		dir = slot.Previous(dir)
	}
	stored, found, err := slot.Read(dir)
	if err != nil {
		return nil, fmt.Errorf("read the config slot: %w", err)
	}
	// A write interrupted between the previous link and the slot's leaves both naming one set,
	// which is no config to roll back to, as Status and RollbackConfig say.
	if found && req.Msg.Previous {
		version, err := slot.Version(running)
		if err != nil {
			return nil, fmt.Errorf("read the config's version: %w", err)
		}
		found = stored.Version != "" && stored.Version != version
	}
	switch {
	case !found && req.Msg.Previous:
		return nil, layer.Errorf(connect.CodeNotFound, "the node keeps no previous config of the running image")
	case !found && pending:
		return nil, layer.Errorf(connect.CodeNotFound, "no image is staged, and the node holds no pending config")
	case !found:
		return nil, layer.Errorf(connect.CodeNotFound, "STATE holds no config for the image")
	}
	e, err := config.Encode(config.Redact(stored.Delivered))
	if err != nil {
		return nil, fmt.Errorf("encode the config: %w", err)
	}
	return connect.NewResponse(&nodev1.GetConfigResponse{Documents: e.Text, Version: stored.Version}), nil
}
