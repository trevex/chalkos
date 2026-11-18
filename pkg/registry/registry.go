// Package registry is chalkos's OCI registry: distribution's registry handlers with its
// filesystem storage, served over TLS with the node certificate. Anyone reads; a client
// certificate of the admin role writes and deletes, and collects garbage, which runs on request
// alone: nothing deletes an image on its own.
package registry

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry/handlers"
	"github.com/distribution/distribution/v3/registry/storage"
	storagedriver "github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/distribution/v3/registry/storage/driver/factory"
	// The filesystem driver alone, which registers itself with the factory.
	_ "github.com/distribution/distribution/v3/registry/storage/driver/filesystem"
	"github.com/sirupsen/logrus"

	"github.com/trevex/chalkos/pkg/pki"
)

// GCPath is where an admin's POST collects garbage.
const GCPath = "/chalkos/v1/gc"

// GCResult is the answer to a collection.
type GCResult struct {
	FreedBytes int64 `json:"freedBytes"`
}

// ErrCollecting is a collection asked for while another runs.
var ErrCollecting = errors.New("the registry is collecting garbage already")

// Config is where the registry keeps its images and finds its TLS files.
type Config struct {
	// Storage is the directory of the images.
	Storage string
	// Certificate holds the node certificate's chain and its key, as chalkd's node.pem does; CA
	// the OS CAs client certificates are verified by. The registry reads both again whenever
	// chalkd replaces them.
	Certificate, CA string
}

// Server is the registry, an http.Handler served with TLSConfig.
type Server struct {
	app       http.Handler
	driver    storagedriver.StorageDriver
	namespace distribution.Namespace
	storage   string
	// Every write holds writes for reading and a collection for writing; a write that finds it
	// held or wanted is refused rather than waiting for the collection.
	writes     sync.RWMutex
	collecting atomic.Bool
	tls        *credentials
}

// New is the registry of c. Deletion is on, so explicit deletes work. No blob descriptor cache
// is configured, so a blob a collection removed is uploaded again rather than found in a cache.
func New(ctx context.Context, c Config) (*Server, error) {
	creds := &credentials{certificate: c.Certificate, ca: c.CA}
	if _, err := creds.current(); err != nil {
		return nil, err
	}
	cfg := &configuration.Configuration{
		Storage: configuration.Storage{
			"filesystem": configuration.Parameters{"rootdirectory": c.Storage},
			"delete":     configuration.Parameters{"enabled": true},
		},
	}
	// Signs the state of uploads in progress, which one process alone serves.
	cfg.HTTP.Secret = rand.Text()
	// The limits distribution's config parser defaults to, which a config built in Go lacks: a
	// catalog of at most 0 entries refuses every client that asks for a page size.
	cfg.Catalog.MaxEntries = 1000
	cfg.Tags.MaxTags = 1000
	app := handlers.NewApp(ctx, cfg)
	// The collection's own view of the storage, which the app keeps to itself.
	driver, err := factory.Create(ctx, "filesystem", map[string]any{"rootdirectory": c.Storage})
	if err != nil {
		return nil, err
	}
	namespace, err := storage.NewRegistry(ctx, driver, storage.EnableDelete)
	if err != nil {
		return nil, err
	}
	return &Server{app: app, driver: driver, namespace: namespace, storage: c.Storage, tls: creds}, nil
}

// TLSConfig serves the node certificate and verifies a client certificate when a client gives
// one, each handshake with the files as they are then.
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return s.tls.current() },
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == GCPath {
		s.serveCollect(w, r)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		// Reads are anonymous. containerd sends a host's Registry credentials to its mirror
		// endpoints, which mean nothing here.
		r.Header.Del("Authorization")
		s.app.ServeHTTP(w, r)
		return
	}
	if status, err := admin(r); err != nil {
		writeError(w, status, "DENIED", err.Error())
		return
	}
	if !s.writes.TryRLock() {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the registry is collecting garbage; write again once it finished")
		return
	}
	defer s.writes.RUnlock()
	s.app.ServeHTTP(w, r)
}

// admin refuses a request without a client certificate of the admin role. pki.ClientRole makes a
// certificate of the node CA a node's, whatever its Organization, so no node writes.
func admin(r *http.Request) (int, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return http.StatusUnauthorized, errors.New("writing to the registry needs a client certificate of the admin role")
	}
	role, ok := pki.ClientRole(r.TLS.VerifiedChains)
	if !ok || !role.Allows(pki.RoleAdmin) {
		return http.StatusForbidden, fmt.Errorf("writing to the registry needs a client certificate of the admin role, not of %q", role)
	}
	return 0, nil
}

func (s *Server) serveCollect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "collect garbage with POST")
		return
	}
	if status, err := admin(r); err != nil {
		writeError(w, status, "DENIED", err.Error())
		return
	}
	freed, err := s.Collect(r.Context())
	switch {
	case errors.Is(err, ErrCollecting):
		writeError(w, http.StatusConflict, "UNAVAILABLE", err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(GCResult{FreedBytes: freed})
	}
}

// Collect removes the blobs no manifest references and returns the bytes it freed. It keeps
// untagged manifests. It waits for the writes in flight, refuses new ones while it runs, and
// leaves reads alone. A push whose blobs were uploaded before it and whose manifest comes after
// it fails on the missing blobs and is pushed again.
func (s *Server) Collect(ctx context.Context) (int64, error) {
	if !s.collecting.CompareAndSwap(false, true) {
		return 0, ErrCollecting
	}
	defer s.collecting.Store(false)
	s.writes.Lock()
	defer s.writes.Unlock()
	// MarkAndSweep fails on a storage without the directories of repositories and blobs, which the
	// first push makes.
	for _, dir := range []string{"repositories", "blobs"} {
		if err := os.MkdirAll(filepath.Join(s.storage, "docker/registry/v2", dir), 0o700); err != nil {
			return 0, err
		}
	}
	before, err := usage(s.storage)
	if err != nil {
		return 0, err
	}
	if err := storage.MarkAndSweep(ctx, s.driver, s.namespace, storage.GCOpts{RemoveUntagged: false, Quiet: true}); err != nil {
		return 0, fmt.Errorf("collect garbage: %w", err)
	}
	after, err := usage(s.storage)
	if err != nil {
		return 0, err
	}
	return before - after, nil
}

// usage is the size of the regular files below dir.
func usage(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err == nil {
			n += info.Size()
		}
		return err
	})
	return n, err
}

// writeError answers as the distribution spec's errors do.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": message}}})
}

// QuietLogs has the process's logrus logger, which distribution's handlers log through, write
// warnings and errors alone, and none about a request answered with a client error: a pull of
// an image the registry lacks is one, and containerd's mirror lookups make them all the time.
func QuietLogs() {
	logrus.SetLevel(logrus.WarnLevel)
	logrus.SetFormatter(quiet{logrus.StandardLogger().Formatter})
}

// quiet drops the lines of requests answered with a status below 500.
type quiet struct{ logrus.Formatter }

func (q quiet) Format(e *logrus.Entry) ([]byte, error) {
	if status, ok := e.Data["http.response.status"].(int); ok && status < http.StatusInternalServerError {
		return nil, nil
	}
	return q.Formatter.Format(e)
}
