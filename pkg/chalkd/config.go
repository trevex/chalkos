package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"

	"connectrpc.com/connect"

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
		if err := store.Write(dir, *next); err != nil {
			return nil, failed(connect.CodeInternal, "store the config: %v", err)
		}
		if after, err = config.KindHashes(next.Documents, schemas); err != nil {
			return nil, failed(connect.CodeInternal, "hash the config: %v", err)
		}
	} else if err := store.Remove(dir); err != nil {
		return nil, failed(connect.CodeInternal, "remove the stored config: %v", err)
	}
	if err := s.Identity.LoadConfig(); err != nil {
		return nil, failed(connect.CodeInternal, "write the config for its consumers: %v", err)
	}
	consumers, err := identity.ReadConsumers(s.Identity.Consumers)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	units := identity.KindRestarts(consumers, before, after)
	for _, unit := range units {
		if _, err := s.Run.Run(ctx, "systemctl", "try-restart", unit); err != nil {
			return nil, failed(connect.CodeInternal, "restart %s: %v", unit, err)
		}
	}
	return units, nil
}
