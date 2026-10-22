// Package nodeconfig applies the running image's config slot on the node, at boot in
// chalkos-config.service and after every ApplyConfig: it writes the identity the config
// translates to, as
// /run/chalkos/node.json, which the node's Kubernetes side reads, the networkd units and chrony's
// time servers, and sets the hostname. It writes the config's documents for the units granted
// config kinds, and the Kubernetes cluster it describes for chalkd, and at boot renders the
// image's outputs.
package nodeconfig

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/uki"
)

// Header marks the networkd units the loader writes, so it removes only its own.
const Header = "# Written by chalkd from the node identity.\n"

// Service is a unit granted config kinds, as /etc/chalkos/services.json records it: chalkd
// writes the documents of its kinds below /run/chalkos/config and the values their secretRefs
// name into the unit's credentials. A grant never restarts the unit; an effect does.
type Service struct {
	Kinds []string `json:"kinds"`
}

// Effect is what a delivery does to the node's services, as /etc/chalkos/effects.json records
// it: when one of the outputs On changed, or the CEL rule When holds, chalkd reloads the units of
// Reload, runs Run, restarts the units of Restart, starting them too with Start, and acts on its
// own loops as Chalkd says, restart or reload.
type Effect struct {
	On      []string `json:"on"`
	When    string   `json:"when"`
	Restart []string `json:"restart"`
	Reload  []string `json:"reload"`
	Run     string   `json:"run"`
	Start   bool     `json:"start"`
	Chalkd  string   `json:"chalkd"`
}

// The values of Effect.Chalkd.
const (
	ChalkdRestart = "restart"
	ChalkdReload  = "reload"
)

// Loader holds the paths the identity is applied to; tests point them at temporary directories.
type Loader struct {
	// Slots is the directory of the node's config slots on STATE, one per image, and Cmdline the
	// running kernel's command line, whose usrhash= names the running image and so its slot.
	Slots, Cmdline string
	// RunDir receives node.json, config/ and credentials/.
	RunDir string
	// NetworkDir receives networkd units; units there take precedence over the image's.
	NetworkDir string
	// Services is the image's map of units to the config kinds they are granted, and Effects
	// what its deliveries do to them.
	Services, Effects string
	// ChronySources receives the time servers as chrony's sources file; empty writes none.
	ChronySources string
	// Schemas are the image's schemas, which tell the secretRef fields of its documents.
	Schemas string
	// KubernetesImage is the image's Kubernetes facts, absent on an image without Kubernetes;
	// ClusterFile receives the cluster the config gives on it, which chalkd's Kubernetes side
	// reads.
	KubernetesImage string
	ClusterFile     string
	SetHostname     func(string) error
	// Outputs is the image's outputs, which Render renders below its root. NodeCertificate is the
	// node certificate on STATE, whose subject is the node's name, which renderers read.
	Outputs         string
	Render          outputs.Renderer
	NodeCertificate string
}

// Default applies the identity on the running node.
func Default() Loader {
	return Loader{
		RunDir:          "/run/chalkos",
		NetworkDir:      "/run/systemd/network",
		Services:        "/etc/chalkos/services.json",
		Effects:         "/etc/chalkos/effects.json",
		ChronySources:   "/run/chalkos/chrony/identity.sources",
		Slots:           "/state/config",
		Cmdline:         "/proc/cmdline",
		Schemas:         "/etc/chalkos/schemas",
		KubernetesImage: "/etc/chalkos/kubernetes/image.json",
		ClusterFile:     "/run/chalkos/kubernetes/cluster.json",
		SetHostname:     func(name string) error { return syscall.Sethostname([]byte(name)) },
		Outputs:         outputs.DefaultPath,
		Render:          outputs.Default(),
		NodeCertificate: "/state/chalkd/node.pem",
	}
}

// identityFields holds the fields of the identity the loader needs.
type identityFields struct {
	Hostname     string            `json:"hostname"`
	NetworkUnits map[string]string `json:"networkUnits"`
	Time         identity.Time     `json:"time"`
}

// Slot is the running image's config slot: the one below Slots named by the root hash of the store
// the kernel command line's usrhash= boots.
func (l Loader) Slot() (string, error) {
	cmdline, err := os.ReadFile(l.Cmdline)
	if err != nil {
		return "", fmt.Errorf("read the kernel command line: %w", err)
	}
	hash, err := uki.UsrHash(string(cmdline))
	if err != nil {
		return "", fmt.Errorf("the running image: %w", err)
	}
	return store.Slot(l.Slots, hash), nil
}

// Load applies the running image's config slot: the identity it holds, then its documents for
// the granted units, then the outputs rendered from them. A node without any slot is not installed, as the installer, and nothing is
// applied. A node whose slots are all another image's, as one booted into an image no install or
// upgrade wrote config for, fails without applying any: the node runs without config, its health
// check fails, and a counted boot falls back to an image that has its own.
func (l Loader) Load() error {
	slots, err := store.Slots(l.Slots)
	if err != nil {
		return fmt.Errorf("list the config slots: %w", err)
	}
	if len(slots) == 0 {
		return nil
	}
	stored, err := l.readSlot()
	if err != nil {
		return err
	}
	if err := l.Apply(stored.Identity); err != nil {
		return err
	}
	if err := l.writeConfig(stored); err != nil {
		return err
	}
	return l.renderAtBoot(stored.Documents)
}

// readSlot reads the running image's slot, which must exist.
func (l Loader) readSlot() (store.Stored, error) {
	dir, err := l.Slot()
	if err != nil {
		return store.Stored{}, err
	}
	stored, found, err := store.Read(dir)
	if err != nil {
		return store.Stored{}, fmt.Errorf("read the config slot %s: %w", dir, err)
	}
	if !found {
		return store.Stored{}, fmt.Errorf("STATE holds no config for the running image %s: no install or upgrade wrote config for it", filepath.Base(dir))
	}
	return stored, nil
}

// LoadConfig writes the running image's slot for the units that read it: its documents, Secrets
// excepted, below RunDir/config, and for each granted unit the values the secretRefs of its kinds
// name below RunDir/credentials/<unit>. On an image with Kubernetes it then writes the cluster the
// documents give.
func (l Loader) LoadConfig() error {
	stored, err := l.readSlot()
	if err != nil {
		return err
	}
	return l.writeConfig(stored)
}

// writeConfig writes a slot's documents as LoadConfig does. It does not check them against the
// image's schemas again: the slot is the image's own, written only once they passed. A slot without
// documents, an identity alone, gets empty directories, so its units load their credentials all
// the same, and no cluster.
func (l Loader) writeConfig(stored store.Stored) error {
	services, err := ReadServices(l.Services)
	if err != nil {
		return err
	}
	// A unit of no kinds loads no credentials, so it gets no directory.
	kinds := map[string][]string{}
	for unit, c := range services {
		if len(c.Kinds) > 0 {
			kinds[unit] = c.Kinds
		}
	}
	found := len(stored.Documents) > 0
	var schemas *schema.Schemas
	if found {
		if schemas, err = schema.LoadSchemas(l.Schemas); err != nil {
			return fmt.Errorf("read the image's schemas: %w", err)
		}
	}
	if err := store.WriteRuntime(l.RunDir, stored.Documents, schemas, kinds); err != nil {
		return err
	}
	var c *kubernetes.Cluster
	if found {
		if c, err = l.Cluster(stored.Documents); err != nil {
			// No Kubernetes side runs on settings the config does not give.
			if rerr := l.WriteCluster(nil); rerr != nil {
				return errors.Join(err, rerr)
			}
			return fmt.Errorf("the node runs no Kubernetes: its stored config gives no Kubernetes cluster for this image: %w; deliver config that does with chalkctl apply-config", err)
		}
	}
	return l.WriteCluster(c)
}

// Cluster returns the Kubernetes cluster docs give on this image, from the image's facts and the
// documents' settings; nil on an image without Kubernetes.
func (l Loader) Cluster(docs []config.Document) (*kubernetes.Cluster, error) {
	if l.KubernetesImage == "" {
		return nil, nil
	}
	img, err := kubernetes.ReadImage(l.KubernetesImage)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := config.ClusterOf(docs, img)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// WriteCluster writes c as the node's cluster file, or removes the file for nil. A file that
// holds c already is left as it is.
func (l Loader) WriteCluster(c *kubernetes.Cluster) error {
	if l.ClusterFile == "" {
		return nil
	}
	if c == nil {
		if err := os.Remove(l.ClusterFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	old, err := os.ReadFile(l.ClusterFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if bytes.Equal(old, data) {
		return nil
	}
	return writeFile(l.ClusterFile, data, 0o644)
}

// Apply writes what the identity determines and sets the hostname.
func (l Loader) Apply(data []byte) error {
	var id identityFields
	if err := json.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("parse the identity: %w", err)
	}
	if err := writeFile(filepath.Join(l.RunDir, "node.json"), data, 0o644); err != nil {
		return err
	}
	if err := l.writeNetworkUnits(id.NetworkUnits); err != nil {
		return err
	}
	if l.ChronySources != "" {
		sources, err := id.Time.ChronySources()
		if err != nil {
			return err
		}
		if err := writeFile(l.ChronySources, []byte(sources), 0o644); err != nil {
			return err
		}
	}
	if id.Hostname != "" {
		if err := l.SetHostname(id.Hostname); err != nil {
			return fmt.Errorf("set the hostname: %w", err)
		}
	}
	return nil
}

// writeNetworkUnits replaces the units written from an earlier identity.
func (l Loader) writeNetworkUnits(units map[string]string) error {
	if err := os.MkdirAll(l.NetworkDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(l.NetworkDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(l.NetworkDir, e.Name())
		if _, keep := units[e.Name()]; keep {
			continue
		}
		if data, err := os.ReadFile(path); err == nil && bytes.HasPrefix(data, []byte(Header)) {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	for name, text := range units {
		if strings.ContainsRune(name, '/') || !(strings.HasSuffix(name, ".network") || strings.HasSuffix(name, ".netdev") || strings.HasSuffix(name, ".link")) {
			return fmt.Errorf("invalid networkd unit name %q", name)
		}
		if err := writeFile(filepath.Join(l.NetworkDir, name), []byte(Header+text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ReadServices reads the image's services.json; an image without one grants no unit a kind.
func ReadServices(path string) (map[string]Service, error) {
	services := map[string]Service{}
	if err := readJSON(path, &services); err != nil {
		return nil, err
	}
	return services, nil
}

// ReadEffects reads the image's effects.json, refusing an effect chalkd could not carry out; an
// image without one has no effects.
func ReadEffects(path string) (map[string]Effect, error) {
	effects := map[string]Effect{}
	if err := readJSON(path, &effects); err != nil {
		return nil, err
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(effects)) {
		if err := effects[name].Check(); err != nil {
			errs = append(errs, fmt.Errorf("%s: effect %s: %w", path, name, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return effects, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// Check refuses an effect that nothing triggers or that does nothing, a unit that is no unit
// name, a value of Chalkd chalkd does not know, and a command that names no program: chalkd
// splits Run on spaces, with no shell quoting, and runs the first word, a program of its PATH or
// of the image's store.
func (e Effect) Check() error {
	if len(e.On) == 0 && e.When == "" {
		return errors.New("it has neither on nor when, so nothing triggers it")
	}
	if len(e.Restart) == 0 && len(e.Reload) == 0 && e.Run == "" && e.Chalkd == "" {
		return errors.New("it restarts, reloads and runs nothing")
	}
	for _, unit := range slices.Concat(e.Restart, e.Reload) {
		if unit == "" || strings.ContainsAny(unit, " /") || !strings.Contains(unit, ".") {
			return fmt.Errorf("%q is no unit name, such as bird.service", unit)
		}
	}
	if e.Chalkd != "" && e.Chalkd != ChalkdRestart && e.Chalkd != ChalkdReload {
		return fmt.Errorf("chalkd is %q, not %s or %s", e.Chalkd, ChalkdRestart, ChalkdReload)
	}
	if e.Run != "" {
		argv := strings.Fields(e.Run)
		switch {
		case len(argv) == 0:
			return errors.New("the command to run is empty")
		case strings.Contains(argv[0], "/") && !strings.HasPrefix(argv[0], "/nix/store/"):
			return fmt.Errorf("the command %q names %s, which is neither a program of the image's store nor a bare program name", e.Run, argv[0])
		}
	}
	return nil
}

func writeFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// RenderOutputs renders and checks every output of the image from docs, a config set with its
// defaults, for the node called name, and stages those that pass; schemas tell the secretRef
// fields of docs, the image's own when nil. It changes nothing a service reads.
func (l Loader) RenderOutputs(ctx context.Context, name string, docs []config.Document, schemas *schema.Schemas) (*outputs.Staged, []outputs.Failure, error) {
	defs, schemas, err := l.outputs(docs, schemas)
	if err != nil {
		return nil, nil, err
	}
	staged, failures := l.Render.Render(ctx, defs, outputs.Input{Node: name, Documents: docs, Schemas: schemas})
	return staged, failures, nil
}

// OutputStates are the states of the outputs the node serves now, which docs, the config they
// were rendered from, references Secrets of; schemas are as for RenderOutputs.
func (l Loader) OutputStates(docs []config.Document, schemas *schema.Schemas) (map[string]outputs.State, error) {
	defs, schemas, err := l.outputs(docs, schemas)
	if err != nil {
		return nil, err
	}
	states := map[string]outputs.State{}
	for name, d := range defs {
		if states[name], err = outputs.Live(d, outputs.Input{Documents: docs, Schemas: schemas}); err != nil {
			return nil, fmt.Errorf("output %s: %w", name, err)
		}
	}
	return states, nil
}

// outputs reads the image's outputs, and the image's schemas in place of nil ones when there are
// outputs and documents to render them from.
func (l Loader) outputs(docs []config.Document, schemas *schema.Schemas) (map[string]outputs.Definition, *schema.Schemas, error) {
	defs, err := outputs.Read(l.Outputs, l.Render.Root)
	if err != nil {
		return nil, nil, err
	}
	if schemas == nil && len(defs) > 0 && len(docs) > 0 {
		if schemas, err = schema.LoadSchemas(l.Schemas); err != nil {
			return nil, nil, fmt.Errorf("read the image's schemas: %w", err)
		}
	}
	return defs, schemas, nil
}

// renderAtBoot renders the outputs of the slot's documents and swaps in every one that passes,
// since /run starts empty: the renderers are pure, so the files are those the delivery checked.
// An output that fails fails the boot's config, which fails its health check; the status names
// it.
func (l Loader) renderAtBoot(docs []config.Document) error {
	if l.Outputs == "" {
		return nil
	}
	staged, failures, err := l.RenderOutputs(context.Background(), l.nodeName(), docs, nil)
	if err != nil {
		return err
	}
	defer staged.Discard()
	errs := []error{staged.Swap()}
	if len(failures) > 0 {
		errs = append(errs, outputs.RecordFailures(l.Render.Root, failures), outputs.Join(failures))
	}
	return errors.Join(errs...)
}

// nodeName is the subject of the node certificate, the name chalkd renders outputs for; empty,
// and logged, when the certificate cannot be read.
func (l Loader) nodeName() string {
	data, err := os.ReadFile(l.NodeCertificate)
	if err != nil {
		log.Printf("the node's name for its outputs: %v", err)
		return ""
	}
	for {
		var block *pem.Block
		if block, data = pem.Decode(data); block == nil {
			log.Printf("the node's name for its outputs: %s holds no certificate", l.NodeCertificate)
			return ""
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			log.Printf("the node's name for its outputs: %v", err)
			return ""
		}
		return cert.Subject.CommonName
	}
}
