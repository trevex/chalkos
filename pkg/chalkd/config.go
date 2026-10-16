package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/schema"
)

// configured is a delivered config set, checked against its image's schemas and translated into
// the node's identity for that image.
type configured struct {
	stored   store.Stored
	identity []byte
}

// configure parses text, YAML config documents, checks them as a set against schemas, fills in
// their defaults and translates them into the identity of a node of the image t describes. The
// set's version is the hash of the documents as delivered, before the defaults, as a client
// computes it from the files it sends.
func configure(text string, schemas *schema.Schemas, t config.Target) (configured, error) {
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		return configured{}, failed(connect.CodeInvalidArgument, "parse the config: %v", err)
	}
	version, err := config.Hash(docs)
	if err != nil {
		return configured{}, failed(connect.CodeInvalidArgument, "hash the config: %v", err)
	}
	set, id, err := translate(docs, schemas, t)
	if err != nil {
		return configured{}, failed(connect.CodeInvalidArgument, "the config: %v", err)
	}
	data, err := json.Marshal(id)
	if err != nil {
		return configured{}, failed(connect.CodeInternal, "encode the identity: %v", err)
	}
	return configured{stored: store.Stored{Documents: set.Documents(), Version: version}, identity: data}, nil
}

// translate checks docs as a set against schemas and translates them into the identity of a node
// of the image t describes: the set rules, each document's schema, the Secrets their secretRefs
// name and, on an image with Kubernetes, the cluster, registry mirrors and manifests they give.
func translate(docs []config.Document, schemas *schema.Schemas, t config.Target) (*config.Set, manifest.Identity, error) {
	set, err := config.NewSet(docs, schemas)
	if err != nil {
		return nil, manifest.Identity{}, err
	}
	if err := store.CheckCredentials(set.Documents(), schemas); err != nil {
		return nil, manifest.Identity{}, err
	}
	id, err := set.Identity(t)
	if err != nil {
		return nil, manifest.Identity{}, err
	}
	if t.Image != nil {
		if err := set.CheckKubernetes(*t.Image); err != nil {
			return nil, manifest.Identity{}, err
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

// applyConfig keeps next as the node's config set, writes it for its consumers and restarts
// those whose kinds changed: a kind changes when one of its documents does, or a Secret one of
// them references. schemas are the running image's, which next was checked against. It returns
// the restarted units.
func (s *Server) applyConfig(ctx context.Context, next store.Stored, schemas *schema.Schemas) ([]string, error) {
	dir := s.Loader.Config
	prev, _, err := store.Read(dir)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the stored config: %v", err)
	}
	before, err := config.KindHashes(prev.Documents, schemas)
	if err != nil {
		return nil, failed(connect.CodeInternal, "hash the stored config: %v", err)
	}
	after, err := config.KindHashes(next.Documents, schemas)
	if err != nil {
		return nil, failed(connect.CodeInternal, "hash the config: %v", err)
	}
	consumers, err := nodeconfig.ReadConsumers(s.Loader.Consumers)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	units := nodeconfig.KindRestarts(consumers, before, after)
	if err := store.Write(dir, next); err != nil {
		return nil, failed(connect.CodeInternal, "store the config: %v", err)
	}
	// From here on STATE holds the new set, so a retry compares it with itself and restarts
	// nothing. A failure therefore names the units that still need a restart, rather than
	// leaving a node whose units read the old config without saying so.
	if err := s.Loader.LoadConfig(); err != nil {
		return nil, failed(connect.CodeInternal, "write the config for its consumers: %v%s", err, restartByHand(units))
	}
	var pending []string
	var errs []error
	for _, unit := range units {
		if err := s.restartConsumer(ctx, unit); err != nil {
			pending = append(pending, unit)
			errs = append(errs, fmt.Errorf("restart %s: %w", unit, err))
		}
	}
	if len(errs) > 0 {
		return nil, failed(connect.CodeInternal, "%v%s", errors.Join(errs...), restartByHand(pending))
	}
	return units, nil
}

// restartConsumer restarts a unit that reads the config if it runs, or ran and stays active. A
// unit that failed, such as one that found no document of its kind at boot, is started again
// too: the new config may be what it lacked, and try-restart leaves a failed unit alone. A unit
// that never started, or was stopped, stays so.
func (s *Server) restartConsumer(ctx context.Context, unit string) error {
	out, err := s.Run.Run(ctx, "systemctl", "show", "--property=ActiveState", "--value", unit)
	if err != nil {
		log.Printf("the state of %s: %v; restarting it only if it runs", unit, err)
	}
	if err != nil || strings.TrimSpace(string(out)) != "failed" {
		_, err := s.Run.Run(ctx, "systemctl", "try-restart", unit)
		return err
	}
	// The failures that put it in its state count against its start limit, which would refuse
	// the restart.
	if _, err := s.Run.Run(ctx, "systemctl", "reset-failed", unit); err != nil {
		return err
	}
	_, err = s.Run.Run(ctx, "systemctl", "restart", unit)
	return err
}

// restartByHand tells the operator which units read a config they have not loaded yet.
func restartByHand(units []string) string {
	if len(units) == 0 {
		return ""
	}
	return fmt.Sprintf("; the config is stored, so restart %s by hand once that is fixed", strings.Join(units, " "))
}

// installConfig translates the config an install header delivers into the identity of the node
// for the image installed, as JSON, and returns it with the config set the node keeps. In place,
// that image is the running one; from the installer, the header describes it.
func (s *Server) installConfig(h *nodev1.InstallHeader) (string, *store.Stored, error) {
	if h.Config == "" {
		return "", nil, failed(connect.CodeInvalidArgument, "the header delivers no config")
	}
	var (
		t       config.Target
		schemas *schema.Schemas
		err     error
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
		return "", nil, err
	}
	c, err := configure(h.Config, schemas, t)
	if err != nil {
		return "", nil, err
	}
	return string(c.identity), &c.stored, nil
}

// headerTarget is the image an install from the installer writes, as its header describes it,
// and the schemas the header carries for it.
func headerTarget(h *nodev1.InstallHeader) (config.Target, *schema.Schemas, error) {
	if h.Image == nil {
		return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the installer needs the role image to write to the target disk")
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
