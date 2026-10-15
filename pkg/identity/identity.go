// Package identity applies a node's identity on the node: it writes /run/chalkos/node.json, which
// the node's Kubernetes side reads, the networkd units and chrony's time servers, and sets the
// hostname. It writes the node's stored config for the units that read config kinds, and tells
// which of them read kinds that changed.
package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/schema"
)

// Header marks the networkd units the loader writes, so it removes only its own.
const Header = "# Written by chalkd from the node identity.\n"

// Consumer is a unit that reads config kinds, as /etc/chalkos/consumers.json records it.
type Consumer struct {
	// Kinds are the config kinds the unit reads, by name.
	Kinds           []string `json:"kinds"`
	RestartOnChange bool     `json:"restartOnChange"`
}

// Loader holds the paths the identity is applied to; tests point them at temporary directories.
type Loader struct {
	// Identity is the node's identity on STATE.
	Identity string
	// RunDir receives node.json, config/ and credentials/.
	RunDir string
	// NetworkDir receives networkd units; units there take precedence over the image's.
	NetworkDir string
	// Consumers is the image's map of units to the config kinds they read.
	Consumers string
	// ChronySources receives the time servers as chrony's sources file; empty writes none.
	ChronySources string
	// Config is the node's config set on STATE, and Schemas the image's schemas, which tell the
	// secretRef fields of its documents.
	Config      string
	Schemas     string
	SetHostname func(string) error
}

// Default applies the identity on the running node.
func Default() Loader {
	return Loader{
		Identity:      "/state/identity.json",
		RunDir:        "/run/chalkos",
		NetworkDir:    "/run/systemd/network",
		Consumers:     "/etc/chalkos/consumers.json",
		ChronySources: "/run/chalkos/chrony/identity.sources",
		Config:        "/state/config",
		Schemas:       "/etc/chalkos/schemas",
		SetHostname:   func(name string) error { return syscall.Sethostname([]byte(name)) },
	}
}

// identity holds the fields of the identity the loader needs.
type identity struct {
	Hostname     string            `json:"hostname"`
	NetworkUnits map[string]string `json:"networkUnits"`
	Time         manifest.Time     `json:"time"`
}

// Load applies the identity recorded on STATE, then writes the node's stored config for its
// consumers. A node without an identity is not installed, and nothing is applied.
func (l Loader) Load() error {
	data, err := os.ReadFile(l.Identity)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := l.Apply(data); err != nil {
		return err
	}
	return l.LoadConfig()
}

// LoadConfig writes the config set stored on STATE for the units that read it: its documents,
// Secrets excepted, below RunDir/config, and for each consumer of kinds the values their
// secretRefs name below RunDir/credentials/<unit>. It does not check the set against the image's
// schemas again: chalkd stored it only once it passed, and a schema that changed since must not
// keep the node from booting. A node without a set gets empty directories, so its units load
// their credentials all the same.
func (l Loader) LoadConfig() error {
	stored, found, err := store.Read(l.Config)
	if err != nil {
		return fmt.Errorf("read the stored config: %w", err)
	}
	consumers, err := ReadConsumers(l.Consumers)
	if err != nil {
		return err
	}
	kinds := map[string][]string{}
	for unit, c := range consumers {
		kinds[unit] = c.Kinds
	}
	var schemas *schema.Schemas
	if found {
		if schemas, err = schema.LoadSchemas(l.Schemas); err != nil {
			return fmt.Errorf("read the image's schemas: %w", err)
		}
	}
	return store.WriteRuntime(l.RunDir, stored.Documents, schemas, kinds)
}

// Apply writes what the identity determines and sets the hostname.
func (l Loader) Apply(data []byte) error {
	var id identity
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
	var a, b identity
	if json.Unmarshal(old, &a) != nil || json.Unmarshal(new, &b) != nil {
		return true
	}
	sa, errA := a.Time.ChronySources()
	sb, errB := b.Time.ChronySources()
	return errA != nil || errB != nil || sa != sb
}

// KindRestarts returns the units to restart after the config changed: those that want restarts
// and read a kind whose hash differs between before and after, config.KindHashes of the old and
// the new set. A kind in neither set is unchanged.
func KindRestarts(consumers map[string]Consumer, before, after map[string]string) []string {
	var units []string
	for unit, c := range consumers {
		if !c.RestartOnChange {
			continue
		}
		for _, kind := range c.Kinds {
			if before[kind] != after[kind] {
				units = append(units, unit)
				break
			}
		}
	}
	sort.Strings(units)
	return units
}
