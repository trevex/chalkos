// Package nodeconfig applies the running image's config slot on the node, at boot in
// chalkos-config.service and after every ApplyConfig: it writes the identity the config
// translates to, as
// /run/chalkos/node.json, which the node's Kubernetes side reads, the networkd units and chrony's
// time servers, and sets the hostname. It writes the config's documents for the units that read
// config kinds, and the Kubernetes cluster it describes for chalkd.
package nodeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/uki"
)

// Header marks the networkd units the loader writes, so it removes only its own.
const Header = "# Written by chalkd from the node identity.\n"

// Consumer is a unit that reads config kinds, or that a delivery restarts or reloads, as
// /etc/chalkos/consumers.json records it.
type Consumer struct {
	// Kinds are the config kinds the unit reads, by name; none for a unit that reads no
	// documents and loads no credentials.
	Kinds   []string `json:"kinds"`
	Restart Restart  `json:"restart"`
	Reload  Reload   `json:"reload"`
}

// Restart is when a delivery restarts a consumer: When is a CEL rule, and Start starts a unit
// that does not run too.
type Restart struct {
	When  string `json:"when"`
	Start bool   `json:"start"`
}

// Reload is when a delivery reloads a consumer, a CEL rule, and the command that reloads it.
type Reload struct {
	When    string `json:"when"`
	Command string `json:"command"`
}

// Loader holds the paths the identity is applied to; tests point them at temporary directories.
type Loader struct {
	// Slots is the directory of the node's config slots on STATE, one per image, and Cmdline the
	// running kernel's command line, whose usrhash= names the running image and so its slot.
	Slots, Cmdline string
	// RunDir receives node.json, config/ and credentials/.
	RunDir string
	// NetworkDir receives networkd units; units there take precedence over the image's.
	NetworkDir string
	// Consumers is the image's map of units to the config kinds they read.
	Consumers string
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
}

// Default applies the identity on the running node.
func Default() Loader {
	return Loader{
		RunDir:          "/run/chalkos",
		NetworkDir:      "/run/systemd/network",
		Consumers:       "/etc/chalkos/consumers.json",
		ChronySources:   "/run/chalkos/chrony/identity.sources",
		Slots:           "/state/config",
		Cmdline:         "/proc/cmdline",
		Schemas:         "/etc/chalkos/schemas",
		KubernetesImage: "/etc/chalkos/kubernetes/image.json",
		ClusterFile:     "/run/chalkos/kubernetes/cluster.json",
		SetHostname:     func(name string) error { return syscall.Sethostname([]byte(name)) },
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
// their consumers. A node without any slot is not installed, as the installer, and nothing is
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
	return l.writeConfig(stored)
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
// excepted, below RunDir/config, and for each consumer of kinds the values their secretRefs name
// below RunDir/credentials/<unit>. On an image with Kubernetes it then writes the cluster the
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
	consumers, err := ReadConsumers(l.Consumers)
	if err != nil {
		return err
	}
	// A unit of no kinds loads no credentials, so it gets no directory.
	kinds := map[string][]string{}
	for unit, c := range consumers {
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
			if _, rerr := l.WriteCluster(nil); rerr != nil {
				return errors.Join(err, rerr)
			}
			return fmt.Errorf("the node runs no Kubernetes: its stored config gives no Kubernetes cluster for this image: %w; deliver config that does with chalkctl apply-config", err)
		}
	}
	_, err = l.WriteCluster(c)
	return err
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

// WriteCluster writes c as the node's cluster file, or removes the file for nil, and reports
// whether its content changed.
func (l Loader) WriteCluster(c *kubernetes.Cluster) (bool, error) {
	if l.ClusterFile == "" {
		return false, nil
	}
	old, err := os.ReadFile(l.ClusterFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if c == nil {
		if err := os.Remove(l.ClusterFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return old != nil, nil
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	if bytes.Equal(old, data) {
		return false, nil
	}
	return true, writeFile(l.ClusterFile, data, 0o644)
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

// ReadConsumers reads the image's consumers.json; an image without one has no consumers.
func ReadConsumers(path string) (map[string]Consumer, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Consumer{}, nil
	}
	if err != nil {
		return nil, err
	}
	var consumers map[string]Consumer
	if err := json.Unmarshal(data, &consumers); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return consumers, nil
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

// TimeChanged reports whether the time servers of two identities differ; an identity that does
// not parse counts as changed.
func TimeChanged(old, new []byte) bool {
	var a, b identityFields
	if json.Unmarshal(old, &a) != nil || json.Unmarshal(new, &b) != nil {
		return true
	}
	sa, errA := a.Time.ChronySources()
	sb, errB := b.Time.ChronySources()
	return errA != nil || errB != nil || sa != sb
}
