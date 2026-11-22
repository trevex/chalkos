// Package registry is chalkos's OCI registry: distribution's registry handlers with its
// filesystem storage, served over TLS with the node certificate. A certificate of the OS CA reads,
// a node's or a client's of any role; one of the admin role writes and deletes, and collects
// garbage, which runs on request alone: nothing deletes an image on its own. A Unix socket on the
// node serves reads without a certificate, to whoever its file mode lets connect.
package registry

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

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

// WriteIdle is how long a write's body may send nothing before the registry ends the request, so
// that a stalled upload does not keep a collection waiting.
const WriteIdle = 5 * time.Minute

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
	// the OS CAs client certificates are verified by. The registry reads both once, at New; its
	// unit restarts it when chalkd replaces them.
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
	cert       tls.Certificate
	// roots are the OS CAs every request's client certificate is verified against again.
	roots atomic.Pointer[x509.CertPool]
}

// New is the registry of c. Deletion is on, so explicit deletes work. No blob descriptor cache
// is configured, so a blob a collection removed is uploaded again rather than found in a cache.
func New(ctx context.Context, c Config) (*Server, error) {
	cert, roots, err := loadCredentials(c.Certificate, c.CA)
	if err != nil {
		return nil, err
	}
	cfg := &configuration.Configuration{
		Storage: configuration.Storage{
			"filesystem": configuration.Parameters{"rootdirectory": c.Storage},
			"delete":     configuration.Parameters{"enabled": true},
			// Removes uploads begun more than a week ago and never finished, once a day, as
			// distribution does by default; never an image.
			"maintenance": configuration.Parameters{"uploadpurging": map[any]any{
				"enabled": true, "age": "168h", "interval": "24h", "dryrun": false,
			}},
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
	s := &Server{app: app, driver: driver, namespace: namespace, storage: c.Storage, cert: cert}
	s.roots.Store(roots)
	return s, nil
}

// TLSConfig serves the node certificate over HTTP/2 or HTTP/1.1 and verifies a client
// certificate when a client gives one. A client without one completes the handshake, and
// ServeHTTP refuses its requests with an answer that says why.
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{s.cert},
		ClientCAs:    s.roots.Load(),
		ClientAuth:   tls.VerifyClientCertIfGiven,
		NextProtos:   []string{"h2", "http/1.1"},
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == GCPath {
		s.serveCollect(w, r)
		return
	}
	if read(r) {
		if status, err := s.client(w, r, "reading", pki.RoleReader); err != nil {
			writeError(w, status, "DENIED", err.Error())
			return
		}
		s.serveRead(w, r)
		return
	}
	if status, err := s.client(w, r, "writing to", pki.RoleAdmin); err != nil {
		writeError(w, status, "DENIED", err.Error())
		return
	}
	if !s.writes.TryRLock() {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the registry is collecting garbage; write again once it finished")
		return
	}
	defer s.writes.RUnlock()
	r.Body = idleBody{r.Body, http.NewResponseController(w)}
	s.app.ServeHTTP(w, r)
}

// read is whether r only reads: a GET or a HEAD.
func read(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

// serveRead serves a read. containerd sends a host's Registry credentials to its mirror
// endpoints, which mean nothing here.
func (s *Server) serveRead(w http.ResponseWriter, r *http.Request) {
	r.Header.Del("Authorization")
	s.app.ServeHTTP(w, r)
}

// client refuses a request, which does what, without a certificate of the OS CA that grants
// need: pki.RoleReader takes a node's or a client's of any role, pki.RoleAdmin a client's of the
// admin role alone. It verifies the certificate again, against the OS CAs and at the time of the
// request, as chalkd does: the handshake's verification would otherwise last as long as the
// client keeps the connection. On a failure the connection closes, so the client's next one takes
// a new handshake. pki.ClientRole makes a certificate of the node CA a node's, whatever its
// Organization, so no node writes.
func (s *Server) client(w http.ResponseWriter, r *http.Request, what string, need pki.Role) (int, error) {
	needs := what + " the registry needs a client certificate of the admin role"
	if need == pki.RoleReader {
		needs = what + " the registry needs a certificate of the OS CA, a node's or a client file's"
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return http.StatusUnauthorized, errors.New(needs)
	}
	chains, err := pki.VerifyClient(r.TLS.PeerCertificates, s.roots.Load(), time.Now())
	if err != nil {
		w.Header().Set("Connection", "close")
		return http.StatusUnauthorized, fmt.Errorf("%s; this one does not verify: %w", needs, err)
	}
	role, ok := pki.ClientRole(chains)
	if !ok || !(role.Allows(need) || need == pki.RoleReader && role == pki.RoleNode) {
		return http.StatusForbidden, fmt.Errorf("%s, not of %q", needs, role)
	}
	return 0, nil
}

// Local serves the reads of a Unix socket on the node without a certificate: the socket's file
// mode decides who connects. It refuses every write.
func (s *Server) Local() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !read(r) {
			writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "the registry's socket serves reads alone; write over TLS with a client certificate of the admin role")
			return
		}
		s.serveRead(w, r)
	})
}

// idleBody ends a write whose client sends nothing for WriteIdle, which releases the collection
// that waits for it.
type idleBody struct {
	io.ReadCloser
	rc *http.ResponseController
}

func (b idleBody) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(WriteIdle))
	return b.ReadCloser.Read(p)
}

func (s *Server) serveCollect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "collect garbage with POST")
		return
	}
	if status, err := s.client(w, r, "writing to", pki.RoleAdmin); err != nil {
		writeError(w, status, "DENIED", err.Error())
		return
	}
	// A collection runs to its end once begun, also when the client goes away.
	freed, err := s.Collect(context.WithoutCancel(r.Context()))
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
// untagged manifests. It waits for the writes in flight, each of which ends after WriteIdle
// without data, refuses new ones while it runs, and leaves reads alone. Writes between requests
// are not held: a push whose blobs were uploaded before it and whose manifest comes after it
// fails on the missing blobs and is pushed again, and an upload begun before it and finished
// after it leaves a blob that the next collection frees unless a manifest references it.
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

// usage is the size of the regular files below dir, apart from uploads in progress, which the
// upload purger may remove while a collection runs.
func usage(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == "_uploads":
			return fs.SkipDir
		case !d.Type().IsRegular():
			return nil
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
// warnings and errors alone, and of a request only that the registry failed it: anyone can make
// requests the registry refuses, a pull of an image it lacks is one, and containerd's mirror
// lookups make them all the time.
func QuietLogs() {
	logrus.SetLevel(logrus.WarnLevel)
	logrus.SetFormatter(quiet{logrus.StandardLogger().Formatter})
}

// quiet drops the lines of a request, which carry its ID, unless they report a status of 500 or
// above.
type quiet struct{ logrus.Formatter }

func (q quiet) Format(e *logrus.Entry) ([]byte, error) {
	if _, ok := e.Data["http.request.id"]; ok {
		if status, _ := e.Data["http.response.status"].(int); status < http.StatusInternalServerError {
			return nil, nil
		}
	}
	return q.Formatter.Format(e)
}

// ErrorLog is the http.Server's error log, written to w without the failed TLS handshakes, which
// anyone who connects can cause.
func ErrorLog(w io.Writer) *log.Logger {
	return log.New(handshakes{w}, "", 0)
}

type handshakes struct{ w io.Writer }

func (h handshakes) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte("http: TLS handshake error")) {
		return len(p), nil
	}
	return h.w.Write(p)
}
