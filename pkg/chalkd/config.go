package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/identity"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
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
	set, err := config.NewSet(docs, schemas)
	if err != nil {
		return configured{}, failed(connect.CodeInvalidArgument, "the config: %v", err)
	}
	if err := store.CheckCredentials(set.Documents(), schemas); err != nil {
		return configured{}, failed(connect.CodeInvalidArgument, "the config: %v", err)
	}
	id, err := set.Identity(t)
	if err != nil {
		return configured{}, failed(connect.CodeInvalidArgument, "the config: %v", err)
	}
	data, err := json.Marshal(id)
	if err != nil {
		return configured{}, failed(connect.CodeInternal, "encode the identity: %v", err)
	}
	return configured{stored: store.Stored{Documents: set.Documents(), Version: version}, identity: data}, nil
}

// runningTarget is the running image, as config for it is translated, and its schemas.
func (s *Server) runningTarget() (config.Target, *schema.Schemas, error) {
	schemas, err := schema.LoadSchemas(s.Identity.Schemas)
	if err != nil {
		return config.Target{}, nil, failed(connect.CodeInternal, "read the image's schemas: %v", err)
	}
	release := readOSRelease(s.Paths.OSRelease)
	t := config.Target{Role: release["CHALKOS_ROLE"], Platform: release["CHALKOS_PLATFORM"]}
	c, err := k8s.ReadCluster(s.Paths.KubernetesCluster)
	switch {
	case err == nil:
		t.Kubernetes = &c
	case !errors.Is(err, fs.ErrNotExist):
		return config.Target{}, nil, failed(connect.CodeInternal, "%v", err)
	}
	return t, schemas, nil
}

// applyConfig keeps next as the node's config set, or removes the node's set when next is nil,
// writes it for its consumers and restarts those whose kinds changed: a kind changes when one of
// its documents does, or a Secret one of them references. schemas are the running image's; nil
// loads them when needed. It returns the restarted units.
func (s *Server) applyConfig(ctx context.Context, next *store.Stored, schemas *schema.Schemas) ([]string, error) {
	dir := s.Identity.Config
	prev, found, err := store.Read(dir)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the stored config: %v", err)
	}
	if next == nil && !found {
		return nil, nil
	}
	if schemas == nil {
		if schemas, err = schema.LoadSchemas(s.Identity.Schemas); err != nil {
			return nil, failed(connect.CodeInternal, "read the image's schemas: %v", err)
		}
	}
	before, err := config.KindHashes(prev.Documents, schemas)
	if err != nil {
		return nil, failed(connect.CodeInternal, "hash the stored config: %v", err)
	}
	after := map[string]string{}
	if next != nil {
		if after, err = config.KindHashes(next.Documents, schemas); err != nil {
			return nil, failed(connect.CodeInternal, "hash the config: %v", err)
		}
	}
	consumers, err := identity.ReadConsumers(s.Identity.Consumers)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	units := identity.KindRestarts(consumers, before, after)
	if next != nil {
		if err := store.Write(dir, *next); err != nil {
			return nil, failed(connect.CodeInternal, "store the config: %v", err)
		}
	} else if err := store.Remove(dir); err != nil {
		return nil, failed(connect.CodeInternal, "remove the stored config: %v", err)
	}
	// From here on STATE holds the new set, so a retry compares it with itself and restarts
	// nothing. A failure therefore names the units that still need a restart, rather than
	// leaving a node whose units read the old config without saying so.
	if err := s.Identity.LoadConfig(); err != nil {
		return nil, failed(connect.CodeInternal, "write the config for its consumers: %v%s", err, restartByHand(units))
	}
	var pending []string
	var errs []error
	for _, unit := range units {
		if _, err := s.Run.Run(ctx, "systemctl", "try-restart", unit); err != nil {
			pending = append(pending, unit)
			errs = append(errs, fmt.Errorf("restart %s: %w", unit, err))
		}
	}
	if len(errs) > 0 {
		return nil, failed(connect.CodeInternal, "%v%s", errors.Join(errs...), restartByHand(pending))
	}
	return units, nil
}

// restartByHand tells the operator which units read a config they have not loaded yet.
func restartByHand(units []string) string {
	if len(units) == 0 {
		return ""
	}
	return fmt.Sprintf("; the config is stored, so restart %s by hand once that is fixed", strings.Join(units, " "))
}

// installIdentity returns the identity an install header delivers, as JSON, and the config set
// the node keeps: the header's identity and no set, or the identity its config translates to for
// the image installed, and that config. In place, that image is the running one; from the
// installer, the header describes it.
func (s *Server) installIdentity(h *nodev1.InstallHeader) (string, *store.Stored, error) {
	switch {
	case h.Config != "" && h.Identity != "":
		return "", nil, failed(connect.CodeInvalidArgument, "the header delivers both config and an identity; deliver one of them")
	case h.Config == "" && h.Identity == "":
		return "", nil, failed(connect.CodeInvalidArgument, "the header delivers no identity or config")
	case h.Config == "":
		return h.Identity, nil, nil
	}
	var (
		t       config.Target
		schemas *schema.Schemas
		err     error
	)
	switch {
	case s.Installer:
		t, schemas, err = headerTarget(h)
	case len(h.Schemas) > 0 || len(h.KubernetesCluster) > 0:
		err = failed(connect.CodeInvalidArgument, "an install in place checks the config against the running image; the header must carry no schemas or kubernetes.json")
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
	schemas, err := schemasFrom(h.Schemas)
	if err != nil {
		return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the image's schemas: %v", err)
	}
	t := config.Target{Role: h.Image.GetRole(), Platform: h.Image.GetPlatform()}
	if len(h.KubernetesCluster) > 0 {
		c, err := k8s.ParseCluster(h.KubernetesCluster)
		if err != nil {
			return config.Target{}, nil, failed(connect.CodeInvalidArgument, "the image's kubernetes.json: %v", err)
		}
		t.Kubernetes = &c
	}
	return t, schemas, nil
}

// schemasFrom loads schemas a request carries by file name, as an image directory's schemas/
// holds them.
func schemasFrom(files map[string][]byte) (*schema.Schemas, error) {
	dir, err := os.MkdirTemp("", "chalkd-schemas-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for name, data := range files {
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("%q is not the name of a schema file", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return nil, err
		}
	}
	return schema.LoadSchemas(dir)
}
