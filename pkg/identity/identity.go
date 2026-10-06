// Package identity applies a node's identity on the node: it writes /run/chalkos/node.json, one
// credential file per identity key that a unit reads, and the networkd units, and sets the
// hostname. It also tells which units read keys that changed.
package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Header marks the networkd units the loader writes, so it removes only its own.
const Header = "# Written by chalkd from the node identity.\n"

// IdentityVersion names an identity document by its SHA-256, so a client can tell whether a node
// runs the identity it would deliver.
func IdentityVersion(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Consumer is a unit that reads identity keys, as /etc/chalkos/consumers.json records it.
type Consumer struct {
	Keys            []string `json:"keys"`
	RestartOnChange bool     `json:"restartOnChange"`
}

// Loader holds the paths the identity is applied to; tests point them at temporary directories.
type Loader struct {
	// Identity is the node's identity on STATE.
	Identity string
	// RunDir receives node.json and credentials/.
	RunDir string
	// NetworkDir receives networkd units; units there take precedence over the image's.
	NetworkDir string
	// Consumers is the image's map of units to the keys they read.
	Consumers   string
	SetHostname func(string) error
}

// Default applies the identity on the running node.
func Default() Loader {
	return Loader{
		Identity:    "/state/identity.json",
		RunDir:      "/run/chalkos",
		NetworkDir:  "/run/systemd/network",
		Consumers:   "/etc/chalkos/consumers.json",
		SetHostname: func(name string) error { return syscall.Sethostname([]byte(name)) },
	}
}

// identity holds the fields of the identity the loader needs.
type identity struct {
	Hostname     string                     `json:"hostname"`
	NetworkUnits map[string]string          `json:"networkUnits"`
	Extensions   map[string]json.RawMessage `json:"extensions"`
}

// Load applies the identity recorded on STATE. A node without one is not installed, and nothing
// is applied.
func (l Loader) Load() error {
	data, err := os.ReadFile(l.Identity)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return l.Apply(data)
}

// Apply writes what the identity determines and sets the hostname.
func (l Loader) Apply(data []byte) error {
	var id identity
	if err := json.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("parse the identity: %w", err)
	}
	consumers, err := ReadConsumers(l.Consumers)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(l.RunDir, "node.json"), data, 0o644); err != nil {
		return err
	}
	if err := l.writeCredentials(data, consumers); err != nil {
		return err
	}
	if err := l.writeNetworkUnits(id.NetworkUnits); err != nil {
		return err
	}
	if id.Hostname != "" {
		if err := l.SetHostname(id.Hostname); err != nil {
			return fmt.Errorf("set the hostname: %w", err)
		}
	}
	return nil
}

// writeCredentials writes one file per key a consumer reads, named like the key. Strings are
// written as they are, other values as JSON. A key the identity lacks has no file, so the unit
// fails to start rather than reading a stale value.
func (l Loader) writeCredentials(data []byte, consumers map[string]Consumer) error {
	dir := filepath.Join(l.RunDir, "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keys := map[string]bool{}
	for _, c := range consumers {
		for _, k := range c.Keys {
			keys[k] = true
		}
	}
	for key := range keys {
		if strings.ContainsRune(key, '/') || strings.HasPrefix(key, ".") {
			return fmt.Errorf("invalid identity key %q", key)
		}
		path := filepath.Join(dir, key)
		value, ok, err := Lookup(data, key)
		if err != nil {
			return err
		}
		if !ok {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := writeFile(path, value, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Lookup returns the value of a dotted identity key: extension values such as "rack.location"
// first, then the identity's own fields such as "hostname". Strings come back unquoted.
func Lookup(data []byte, key string) (value []byte, ok bool, err error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("parse the identity: %w", err)
	}
	path := strings.Split(key, ".")
	v, ok := walk(doc["extensions"], path)
	if !ok {
		v, ok = walk(doc, path)
	}
	if !ok {
		return nil, false, nil
	}
	if s, isString := v.(string); isString {
		return []byte(s), true, nil
	}
	out, err := json.Marshal(v)
	return out, true, err
}

func walk(v any, path []string) (any, bool) {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[p]; !ok {
			return nil, false
		}
	}
	return v, true
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

// Restarts returns the units to restart after the identity changed from old to new: those
// that want restarts and read a key whose value differs.
func Restarts(consumers map[string]Consumer, old, new []byte) ([]string, error) {
	var units []string
	for unit, c := range consumers {
		if !c.RestartOnChange {
			continue
		}
		for _, key := range c.Keys {
			before, hadBefore, err := Lookup(old, key)
			if err != nil {
				return nil, err
			}
			after, hasAfter, err := Lookup(new, key)
			if err != nil {
				return nil, err
			}
			if hadBefore != hasAfter || !bytes.Equal(before, after) {
				units = append(units, unit)
				break
			}
		}
	}
	sort.Strings(units)
	return units, nil
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
