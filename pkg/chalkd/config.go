package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/identity"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/uki"
)

// configure parses text, YAML config documents, and makes them the config slot of the image t
// describes for the node called name, as translateSlot does.
func configure(name, text string, schemas *schema.Schemas, t config.Target) (store.Stored, error) {
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		return store.Stored{}, failed(connect.CodeInvalidArgument, "parse the config: %v", err)
	}
	c, err := translateSlot(name, docs, schemas, t)
	if err != nil {
		return store.Stored{}, failed(connect.CodeInvalidArgument, "the config: %v", err)
	}
	return c, nil
}

// translateSlot checks docs, config documents as delivered, as a set against schemas, fills in
// their defaults and translates them into the identity of the node called name on the image t
// describes: what that image's config slot keeps. The set's version is the hash of the documents
// as delivered, before the defaults, as a client computes it from the files it sends.
func translateSlot(name string, docs []config.Document, schemas *schema.Schemas, t config.Target) (store.Stored, error) {
	version, err := config.Hash(docs)
	if err != nil {
		return store.Stored{}, fmt.Errorf("hash the config: %w", err)
	}
	set, id, err := translate(name, docs, schemas, t)
	if err != nil {
		return store.Stored{}, err
	}
	data, err := json.Marshal(id)
	if err != nil {
		return store.Stored{}, fmt.Errorf("encode the identity: %w", err)
	}
	return store.Stored{Delivered: docs, Documents: set.Documents(), Version: version, Identity: data}, nil
}

// translate checks docs as a set against schemas and translates them into the identity of the node
// called name on the image t describes: the set rules, each document's schema, the Secrets their
// secretRefs name, the Registry and Mirror documents and, on an image with Kubernetes, the
// cluster and manifests they give.
func translate(name string, docs []config.Document, schemas *schema.Schemas, t config.Target) (*config.Set, identity.Identity, error) {
	set, err := config.NewSet(docs, schemas)
	if err != nil {
		return nil, identity.Identity{}, err
	}
	if err := set.CheckRegistries(); err != nil {
		return nil, identity.Identity{}, err
	}
	id, err := set.Identity(name, t)
	if err != nil {
		return nil, identity.Identity{}, err
	}
	if t.Image != nil {
		if err := set.CheckKubernetes(*t.Image); err != nil {
			return nil, identity.Identity{}, err
		}
	}
	return set, id, nil
}

// runningTarget is the running image, as config for it is translated, and its schemas.
func (s *Server) runningTarget() (config.Target, *schema.Schemas, error) {
	schemas, err := schema.LoadSchemas(s.Loader.Schemas)
	if err != nil {
		return config.Target{}, nil, failed(connect.CodeFailedPrecondition, "read the image's schemas: %v", err)
	}
	release := readOSRelease(s.Paths.OSRelease)
	t := config.Target{Platform: release["CHALKOS_PLATFORM"]}
	// The facts the node builds its cluster from at boot, so the config is checked against
	// exactly those; an image without them runs no Kubernetes.
	img, err := k8s.ReadImage(s.Loader.KubernetesImage)
	switch {
	case err == nil:
		t.Image = &img
	case !errors.Is(err, fs.ErrNotExist):
		return config.Target{}, nil, failed(connect.CodeInternal, "the image's Kubernetes facts: %v", err)
	}
	return t, schemas, nil
}

// installConfig translates the config an install header delivers into the identity of the node
// for the image installed, and returns the config slot of that image, identity included. In place,
// that image is the running one; from the installer, the header describes it. The config meets
// that image's rules with no running config to compare with: rules of a field that read oldSelf
// do not run, the rules of a kind see no oldDocuments, and every fact is null: an install in place
// that is tried again must not read what the attempt before it left on STATE.
func (s *Server) installConfig(ctx context.Context, h *nodev1.InstallHeader) (store.Stored, error) {
	if h.Config == "" {
		return store.Stored{}, failed(connect.CodeInvalidArgument, "the header delivers no config")
	}
	name, err := headerName(h)
	if err != nil {
		return store.Stored{}, err
	}
	var (
		t       config.Target
		schemas *schema.Schemas
	)
	switch {
	case s.Installer:
		t, schemas, err = headerTarget(h)
	case len(h.Schemas) > 0 || len(h.KubernetesImage) > 0:
		err = failed(connect.CodeInvalidArgument, "an install in place checks the config against the running image; the header must carry no schemas or image.json")
	default:
		t, schemas, err = s.runningTarget()
	}
	if err != nil {
		return store.Stored{}, err
	}
	stored, err := configure(name, h.Config, schemas, t)
	if err != nil {
		return store.Stored{}, err
	}
	set, err := compileRules(schemas, !s.Installer)
	if err != nil {
		return store.Stored{}, err
	}
	f, err := s.factsFor(ctx, stored.Documents, nil, h.FallbackSecret)
	if err != nil {
		return store.Stored{}, err
	}
	f.Install = true
	if err := s.checkRules(set, name, stored.Documents, nil, f); err != nil {
		return store.Stored{}, err
	}
	// In place, the running image's renderers check the outputs before the install changes
	// anything; the node renders them again at the boot that follows. The installer cannot run the
	// installed image's, so that image renders its outputs at its first boot alone.
	if !s.Installer {
		staged, err := s.renderOutputs(ctx, name, stored.Documents, schemas, true)
		if err != nil {
			return store.Stored{}, err
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

// headerTarget is the image an install from the installer writes, as its header describes it,
// and the schemas the header carries for it.
func headerTarget(h *nodev1.InstallHeader) (config.Target, *schema.Schemas, error) {
	if h.Image == nil {
		return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the installer needs the node's image to write to the target disk")
	}
	if len(h.Schemas) == 0 {
		return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the header carries config but not the schemas of the image it installs")
	}
	schemas, err := schema.LoadFiles(h.Schemas)
	if err != nil {
		return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the image's schemas: %v", err)
	}
	t, err := imageTarget(h.Image.GetPlatform(), h.KubernetesImage)
	return t, schemas, err
}

// imageTarget is the image of the platform an install or upgrade writes, as config for it is
// translated, with its image.json, empty for an image without Kubernetes.
func imageTarget(platform string, kubernetesImage []byte) (config.Target, error) {
	t := config.Target{Platform: platform}
	if len(kubernetesImage) > 0 {
		facts, err := k8s.ParseImage(kubernetesImage)
		if err != nil {
			return config.Target{}, failed(connect.CodeInvalidArgument, "the image's image.json: %v", err)
		}
		t.Image = &facts
	}
	return t, nil
}

// runningSlot is the running image's config slot on STATE and what it holds; found is false while
// it holds nothing.
func (s *Server) runningSlot() (dir string, stored store.Stored, found bool, err error) {
	if dir, err = s.Loader.Slot(); err != nil {
		return "", store.Stored{}, false, failed(connect.CodeInternal, "find the running image's config slot: %v", err)
	}
	if stored, found, err = store.Read(dir); err != nil {
		return "", store.Stored{}, false, failed(connect.CodeInternal, "read the running image's config slot: %v", err)
	}
	return dir, stored, found, nil
}

// runningHash is the root hash of the store the node runs, as the kernel command line names it.
func (s *Server) runningHash() ([]byte, error) {
	cmdline, err := os.ReadFile(s.Paths.Cmdline)
	if err != nil {
		return nil, fmt.Errorf("read the kernel command line: %w", err)
	}
	return uki.UsrHash(string(cmdline))
}

// GetConfig returns the documents a config slot keeps as they were delivered, the running
// image's or the staged image's, with every secret value replaced by its hash, so chalkctl can
// show what a delivery changes. It needs the role ApplyConfig needs: names, addresses and the
// shape of the config tell an attacker as much as the values would.
func (s *Server) GetConfig(ctx context.Context, req *connect.Request[nodev1.GetConfigRequest]) (*connect.Response[nodev1.GetConfigResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.Loader.Slot()
	if err != nil {
		return nil, failed(connect.CodeInternal, "find the running image's config slot: %v", err)
	}
	if req.Msg.Staged {
		staged, err := s.stagedImage()
		if err != nil {
			return nil, failed(connect.CodeInternal, "find the staged image: %v", err)
		}
		if staged == nil {
			return nil, failed(connect.CodeNotFound, "no image is staged to boot next")
		}
		dir = store.Slot(s.Loader.Slots, staged)
	}
	stored, found, err := store.Read(dir)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the config slot: %v", err)
	}
	if !found {
		return nil, failed(connect.CodeNotFound, "STATE holds no config for the image")
	}
	e, err := config.Encode(config.Redact(stored.Delivered))
	if err != nil {
		return nil, failed(connect.CodeInternal, "encode the config: %v", err)
	}
	return connect.NewResponse(&nodev1.GetConfigResponse{Documents: e.Text, Version: stored.Version}), nil
}
