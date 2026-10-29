// Package slot keeps a node's config on STATE, a slot per image, and writes it for the node's
// services: the documents below /run/chalkos/config and, for each unit that reads config kinds,
// the secret values those kinds reference, as files systemd loads as the unit's credentials.
package slot

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
	Delivered config.Documents
	// Documents are the same documents with the image's schema defaults filled in, which the
	// node's services read.
	Documents config.Documents
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

// previousSuffix ends the name of the link beside a slot that names the slot's previous config.
const previousSuffix = ".previous"

// Previous is the link beside the slot dir that names the slot's previous config, such as
// /state/config/.<root hash>.previous: the set WriteKeepingPrevious replaced last. Read and
// Version read it as they read a slot; it does not exist while the slot keeps no previous config.
func Previous(dir string) string {
	return filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+previousSuffix)
}

// Dir is the slot of the image whose store has the root hash, below root, such as
// /state/config.
func Dir(root string, rootHash []byte) string {
	return filepath.Join(root, hex.EncodeToString(rootHash))
}

// List returns the root hashes of the slots below root, in order; none when root does not
// exist.
func List(root string) ([][]byte, error) {
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
// reader finds the slot whole or not at all, then the directory the link named, the slot's
// previous config and those that interrupted writes of it left. A directory where a slot's link
// would be is refused, as replace refuses it.
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
		// A slot's link is named by its hash; its directories, the links a replace makes and its
		// previous config's link start with a dot, the hash, and "-", ".link" or ".previous".
		name, _, _ := strings.Cut(strings.TrimPrefix(e.Name(), "."), "-")
		name = strings.TrimSuffix(strings.TrimSuffix(name, ".link"), previousSuffix)
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
		if err := removeLink(Previous(dir)); err != nil {
			return err
		}
		// The links go to disk before the directories they named, so a power cut never leaves a
		// link to a removed directory.
		if err := atomicfile.SyncDir(root); err != nil {
			return err
		}
		if err := removeStale(dir); err != nil {
			return err
		}
	}
	return atomicfile.SyncDir(root)
}

// Write replaces the slot dir, such as /state/config/<root hash>, as a whole. The documents as
// delivered go below delivered/, those with defaults below documents/, each to
// <group>/<Kind>/<name>.json and Secrets to secrets/<name>.json, readable by root alone; the
// identity goes to identity.json, readable by root alone, and the version to version. See
// replace for how a power cut leaves either slot whole. A slot Write writes keeps no previous
// config: one WriteKeepingPrevious kept goes, its link before its directory.
func Write(dir string, s Stored) error {
	if err := checkLink(dir); err != nil {
		return err
	}
	if err := removeLink(Previous(dir)); err != nil {
		return err
	}
	return replace(dir, fill(s))
}

// WriteKeepingPrevious writes s to the slot dir as Write does and keeps the set the slot held as
// its previous config, which Previous names, one level deep: the previous config before it goes.
// A slot that holds s's version already keeps its previous config as it is, so a delivery sent
// twice does not lose it; a slot that holds no set is written as Write writes it. The previous
// link is renamed and synced after the new set is on disk and before the slot's link, and a
// directory goes only once no link names it, so a power cut leaves the slot whole and its
// previous config either the set before or the set the slot still holds.
func WriteKeepingPrevious(dir string, s Stored) error {
	prev := Previous(dir)
	if err := checkLink(dir); err != nil {
		return err
	}
	if err := checkLink(prev); err != nil {
		return err
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return Write(dir, s)
	} else if err != nil {
		return err
	}
	held, err := os.Readlink(dir)
	if err != nil {
		return err
	}
	version, err := Version(dir)
	if err != nil {
		return err
	}
	kept, err := os.Readlink(prev)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if version == s.Version {
		if kept == "" {
			return replace(dir, fill(s))
		}
		return replace(dir, fill(s), filepath.Join(filepath.Dir(dir), kept))
	}
	// The previous link moves once the new set is filled and on disk, so a set that cannot be
	// written keeps the previous config. A power cut between that rename and the slot link's
	// leaves both links naming the held set, which callers take for no previous config.
	moved := false
	err = replaceAfter(dir, fill(s), func() error {
		moved = true
		return relink(prev, held)
	}, filepath.Join(filepath.Dir(dir), held))
	if err == nil || !moved {
		return err
	}
	// While the slot's link still names the held set, the previous link goes back to the set it
	// named, which is still there: only a replace that got through removes it.
	if now, rerr := os.Readlink(dir); rerr != nil || now != held {
		return err
	}
	restore := func() error { return removeLink(prev) }
	if kept != "" {
		restore = func() error { return relink(prev, kept) }
	}
	if rerr := restore(); rerr != nil {
		return errors.Join(err, fmt.Errorf("keep the previous config: %w", rerr))
	}
	return err
}

// RestorePrevious makes the slot dir's previous config its config again, and the set the slot
// held its previous one, as a boot that ends a config's trial does. It writes no set: the two links
// trade places. The slot's link moves first, through the temporary name replace gives it, so a
// power cut before the previous link follows leaves both naming the restored set, which callers
// take for no previous config. The set the slot held may then go with the next write, never the
// restored one.
func RestorePrevious(dir string) error {
	prev := Previous(dir)
	if err := checkLink(dir); err != nil {
		return err
	}
	if err := checkLink(prev); err != nil {
		return err
	}
	held, err := os.Readlink(dir)
	if err != nil {
		return err
	}
	kept, err := os.Readlink(prev)
	if errors.Is(err, fs.ErrNotExist) || err == nil && kept == held {
		return fmt.Errorf("%s keeps no previous config", dir)
	}
	if err != nil {
		return err
	}
	// A previous link whose set is gone would leave the slot's link dangling.
	if _, err := os.Stat(prev); err != nil {
		return err
	}
	if err := relinkVia(dir, filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".link"), kept); err != nil {
		return err
	}
	return relink(prev, held)
}

// fill fills a new slot's directory with s.
func fill(s Stored) func(tmp string) error {
	return func(tmp string) error {
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
	}
}

// relink makes link a relative symbolic link to target, a name in link's directory, in one rename,
// and syncs the directory.
func relink(link, target string) error {
	return relinkVia(link, link+".link", target)
}

// relinkVia is relink with tmp as the new link that is renamed over link.
func relinkVia(link, tmp, target string) error {
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(link))
}

// removeLink removes the link and the link an interrupted relink of it left, after checkLink.
func removeLink(link string) error {
	if err := checkLink(link); err != nil {
		return err
	}
	for _, p := range []string{link, link + ".link"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
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
// system is mounted, as the installer mounts a target's STATE elsewhere than /state. The
// directories keep names are left too, as a slot's previous config.
func replace(dir string, fill func(tmp string) error, keep ...string) error {
	return replaceAfter(dir, fill, nil, keep...)
}

// replaceAfter replaces dir as replace does and calls filled, unless nil, once the new set is on
// disk and before dir's link moves; when filled fails, dir stays as it was.
func replaceAfter(dir string, fill func(tmp string) error, filled func() error, keep ...string) error {
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
	if filled != nil {
		if err := filled(); err != nil {
			os.RemoveAll(tmp)
			return err
		}
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
	return removeStale(dir, append(keep, tmp)...)
}

// removeStale removes the directories next to dir that earlier or interrupted writes left, all
// but those keep names.
func removeStale(dir string, keep ...string) error {
	stale, err := filepath.Glob(filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+"-*"))
	if err != nil {
		return err
	}
	for _, p := range stale {
		if !slices.Contains(keep, p) {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkLink fails when something other than a symbolic link is at dir. Nothing here writes
// anything else there, so it must not replace or remove it.
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

// fillDir makes tmp readable by all, fills it and syncs every directory in it, tmp included, so
// the new set is on disk before a link names it.
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
// other document to <group>/<Kind>/<name>.json, Addons readable by root alone. stored is set for
// the set the node keeps on STATE; the copy for the node's services leaves out Secrets and Addons.
func writeDocuments(root string, docs []config.Document, stored bool) error {
	for _, d := range docs {
		if !stored && d.IsAddon() {
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
		// An Addon may hold Kubernetes Secrets, so root alone reads it.
		if d.IsAddon() {
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
// hidden ones this package uses itself.
func validName(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, "/\x00") && len(s) <= 255
}
