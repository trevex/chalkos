// Package store keeps a node's config on STATE, a slot per image, and writes it for the node's
// services: the documents below /run/chalkos/config and, for each unit that reads config kinds,
// the secret values those kinds reference, as files systemd loads as the unit's credentials.
package store

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
)

// Stored is the config an image's slot on STATE keeps.
type Stored struct {
	// Delivered are the documents as they were delivered, Secrets included, which a later config
	// is compared with.
	Delivered []config.Document
	// Documents are the same documents with the image's schema defaults filled in, which the
	// node's services read.
	Documents []config.Document
	// Version is config.Hash of the documents as they were delivered.
	Version string
	// Identity is the node's identity the documents translate to on the slot's image, as JSON,
	// which the boot applies.
	Identity []byte
}

const (
	versionFile  = "version"
	identityFile = "identity.json"
	secretsDir   = "secrets"
	deliveredDir = "delivered"
	documentsDir = "documents"
)

// Slot is the slot of the image whose store has the root hash, below root, such as
// /state/config.
func Slot(root string, rootHash []byte) string {
	return filepath.Join(root, hex.EncodeToString(rootHash))
}

// Slots returns the root hashes of the slots below root, in order; none when root does not
// exist.
func Slots(root string) ([][]byte, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hashes [][]byte
	for _, e := range entries {
		if h, ok := slotHash(e.Name()); ok && e.Type()&fs.ModeSymlink != 0 {
			hashes = append(hashes, h)
		}
	}
	slices.SortFunc(hashes, bytes.Compare)
	return hashes, nil
}

// slotHash is the root hash a slot's name gives; false for a name that is no root hash.
func slotHash(name string) ([]byte, bool) {
	h, err := hex.DecodeString(name)
	return h, err == nil && len(h) > 0 && name == hex.EncodeToString(h)
}

// Retire removes every slot below root but those of the root hashes kept: its link first, so a
// reader finds the slot whole or not at all, then the directory the link named and those that
// interrupted writes of it left. A directory where a slot's link would be is refused, as
// replace refuses it.
func Retire(root string, keep [][]byte) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, e := range entries {
		// A slot's link is named by its hash; its directories and the link a replace makes start
		// with a dot, the hash, and "-" or ".link".
		name, _, _ := strings.Cut(strings.TrimPrefix(e.Name(), "."), "-")
		name = strings.TrimSuffix(name, ".link")
		if _, ok := slotHash(name); ok {
			names[name] = true
		}
	}
	for _, k := range keep {
		delete(names, hex.EncodeToString(k))
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		dir := filepath.Join(root, name)
		if err := checkLink(dir); err != nil {
			return err
		}
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Remove(filepath.Join(root, "."+name+".link")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := removeStale(dir, ""); err != nil {
			return err
		}
	}
	return atomicfile.SyncDir(root)
}

// Write replaces the slot dir, such as /state/config/<root hash>, as a whole. The documents as
// delivered go below delivered/, those with defaults below documents/, each to
// <group>/<Kind>/<name>.json and Secrets to secrets/<name>.json, readable by root alone; the
// identity goes to identity.json, readable by root alone, and the version to version. See
// replace for how a power cut leaves either slot whole.
func Write(dir string, s Stored) error {
	return replace(dir, func(tmp string) error {
		for _, tree := range []struct {
			dir  string
			docs []config.Document
		}{{deliveredDir, s.Delivered}, {documentsDir, s.Documents}} {
			root := filepath.Join(tmp, tree.dir)
			if err := os.Mkdir(root, 0o755); err != nil {
				return err
			}
			if err := writeDocuments(root, tree.docs, true); err != nil {
				return err
			}
		}
		if err := atomicfile.Replace(filepath.Join(tmp, identityFile), s.Identity, 0o600); err != nil {
			return err
		}
		return atomicfile.Replace(filepath.Join(tmp, versionFile), []byte(s.Version+"\n"), 0o644)
	})
}

// Read returns the slot dir, its documents ordered by apiVersion, kind and name; found is false
// when there is none.
func Read(dir string) (s Stored, found bool, err error) {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return Stored{}, false, nil
	} else if err != nil {
		return Stored{}, false, err
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Stored{}, false, err
	}
	if s.Version, err = Version(root); err != nil {
		return Stored{}, false, err
	}
	if s.Identity, err = os.ReadFile(filepath.Join(root, identityFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Stored{}, false, err
	}
	if s.Delivered, err = readDocuments(filepath.Join(root, deliveredDir)); err != nil {
		return Stored{}, false, err
	}
	if s.Documents, err = readDocuments(filepath.Join(root, documentsDir)); err != nil {
		return Stored{}, false, err
	}
	return s, true, nil
}

// readDocuments reads the documents writeDocuments wrote below root, ordered by apiVersion, kind
// and name; none when root does not exist.
func readDocuments(root string) ([]config.Document, error) {
	var docs []config.Document
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == root {
			return fs.SkipDir
		}
		if err != nil {
			return err
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			return nil
		}
		d, err := readDocument(path)
		if err != nil {
			return err
		}
		docs = append(docs, d)
		return nil
	})
	if err != nil {
		return nil, err
	}
	config.SortDocuments(docs)
	return docs, nil
}

// Version returns the version of the set kept in dir, or "" when there is none.
func Version(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, versionFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// replace fills a new directory next to dir and makes dir, a symbolic link, name it, then removes
// the directory dir named before and any an interrupted replace left. Renaming a link over
// another is atomic where renaming a directory over a non-empty one is impossible, so a reader
// or a power cut finds either set whole. The link is relative, so it resolves wherever the file
// system is mounted, as the installer mounts a target's STATE elsewhere than /state.
func replace(dir string, fill func(tmp string) error) error {
	parent, base := filepath.Dir(dir), filepath.Base(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if err := checkLink(dir); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+base+"-")
	if err != nil {
		return err
	}
	if err := fillDir(tmp, fill); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	link := filepath.Join(parent, "."+base+".link")
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Symlink(filepath.Base(tmp), link); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(link, dir); err != nil {
		os.Remove(link)
		os.RemoveAll(tmp)
		return err
	}
	if err := atomicfile.SyncDir(parent); err != nil {
		return err
	}
	return removeStale(dir, tmp)
}

// removeStale removes the directories next to dir that earlier or interrupted writes left, all
// but keep.
func removeStale(dir, keep string) error {
	stale, err := filepath.Glob(filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+"-*"))
	if err != nil {
		return err
	}
	for _, p := range stale {
		if p != keep {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkLink fails when something other than a symbolic link is at dir, which the store never
// writes there and must not replace or remove.
func checkLink(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return nil
	case info.IsDir():
		return fmt.Errorf("%s is a directory, not a config link", dir)
	default:
		return fmt.Errorf("%s is not a config link", dir)
	}
}

// fillDir makes tmp readable by all, fills it and syncs every directory in it, so the new set is
// on disk before a link names it.
func fillDir(tmp string, fill func(string) error) error {
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := fill(tmp); err != nil {
		return err
	}
	return filepath.WalkDir(tmp, func(path string, e fs.DirEntry, err error) error {
		if err != nil || !e.IsDir() {
			return err
		}
		return atomicfile.SyncDir(path)
	})
}

// writeDocuments writes each document as JSON below root: Secrets to secrets/<name>.json, every
// other document to <group>/<Kind>/<name>.json, those that stay stored, such as Addons, readable
// by root alone. stored is set for the set the node keeps on STATE; the copy for the node's
// services leaves out the Secrets and the documents that stay stored.
func writeDocuments(root string, docs []config.Document, stored bool) error {
	for _, d := range docs {
		if !stored && d.StaysStored() {
			continue
		}
		group, _, _ := strings.Cut(d.APIVersion, "/")
		for _, part := range []string{group, d.Kind, d.Name} {
			if !validName(part) {
				return fmt.Errorf("%s cannot be stored: %q is no file name", d, part)
			}
		}
		data, err := json.Marshal(d.Object)
		if err != nil {
			return fmt.Errorf("%s: %w", d, err)
		}
		data = append(data, '\n')
		if d.IsSecret() {
			if !stored {
				continue
			}
			dir := filepath.Join(root, secretsDir)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := atomicfile.Replace(filepath.Join(dir, d.Name+".json"), data, 0o600); err != nil {
				return err
			}
			continue
		}
		dir, perm := filepath.Join(root, group, d.Kind), fs.FileMode(0o644)
		// A document that stays stored may hold Kubernetes Secrets, so root alone reads it.
		if d.StaysStored() {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				return err
			}
			perm = 0o600
		}
		if err := atomicfile.Replace(filepath.Join(dir, d.Name+".json"), data, perm); err != nil {
			return err
		}
	}
	return nil
}

func readDocument(path string) (config.Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return config.Document{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	// Numbers stay json.Number, as config.Parse gives them, so no digit is rounded away.
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return config.Document{}, fmt.Errorf("%s: %w", path, err)
	}
	return config.FromObject(obj), nil
}

// validName tells whether s can name a file or directory in one path element, other than the
// hidden ones the store uses itself.
func validName(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, "/\x00") && len(s) <= 255
}
