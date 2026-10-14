// Package store keeps a node's config set on STATE and writes it for the node's services: the
// documents below /run/chalkos/config and, for each unit that reads config kinds, the secret
// values those kinds reference, as files systemd loads as the unit's credentials.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
)

// Stored is a node's config set as STATE keeps it.
type Stored struct {
	// Documents are the set's documents with schema defaults filled in, Secrets included.
	Documents []config.Document
	// Version is config.Hash of the documents as they were delivered.
	Version string
}

const (
	versionFile = "version"
	secretsDir  = "secrets"
)

// Write replaces the set kept in dir, such as /state/config, as a whole. Documents go to
// <group>/<Kind>/<name>.json, Secrets to secrets/<name>.json, readable by root alone, and the
// version to version. See replace for how a power cut leaves either set whole.
func Write(dir string, s Stored) error {
	return replace(dir, func(tmp string) error {
		if err := writeDocuments(tmp, s.Documents, true); err != nil {
			return err
		}
		return writeFile(filepath.Join(tmp, versionFile), []byte(s.Version+"\n"), 0o644)
	})
}

// Read returns the set kept in dir, ordered by apiVersion, kind and name; found is false when
// there is none.
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
	if s.Version, err = Version(dir); err != nil {
		return Stored{}, false, err
	}
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
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
		s.Documents = append(s.Documents, d)
		return nil
	})
	if err != nil {
		return Stored{}, false, err
	}
	config.SortDocuments(s.Documents)
	return s, true, nil
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

// Remove removes the set kept in dir: the link first, so the whole set is gone at once, then the
// directory it named.
func Remove(dir string) error {
	target, err := os.Readlink(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dir), target)
	}
	return os.RemoveAll(target)
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
	if err := syncDir(parent); err != nil {
		return err
	}
	stale, err := filepath.Glob(filepath.Join(parent, "."+base+"-*"))
	if err != nil {
		return err
	}
	for _, p := range stale {
		if p != tmp {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	return nil
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
		return syncDir(path)
	})
}

// writeDocuments writes each document as JSON below root: Secrets to secrets/<name>.json when
// withSecrets is set and not at all otherwise, every other document to
// <group>/<Kind>/<name>.json.
func writeDocuments(root string, docs []config.Document, withSecrets bool) error {
	for _, d := range docs {
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
			if !withSecrets {
				continue
			}
			dir := filepath.Join(root, secretsDir)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := writeFile(filepath.Join(dir, d.Name+".json"), data, 0o600); err != nil {
				return err
			}
			continue
		}
		if err := writeFile(filepath.Join(root, group, d.Kind, d.Name+".json"), data, 0o644); err != nil {
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

// writeFile replaces path with data, synced, through a temporary file in its directory.
func writeFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
