package outputs

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// Staged are rendered and checked outputs, waiting below Root/.staging to be swapped in.
type Staged struct {
	root    string
	defs    map[string]Definition
	staging map[string]string
	states  map[string]State
}

// States are the states of the staged outputs, by name.
func (s *Staged) States() map[string]State {
	return maps.Clone(s.states)
}

// Dirs are the directories the staged outputs' files are in, by name, until Swap or Discard.
func (s *Staged) Dirs() map[string]string {
	return maps.Clone(s.staging)
}

// Swap makes each staged output the one its dir links to, replacing the link in one rename so a
// service reads the old files or the new ones and never a mix. The files the old link named are
// removed, and each swapped output's hash is recorded in the status. An output whose dir holds
// anything but an empty directory or a link to an earlier render fails and leaves the dir as it
// is, unless its definition sets Replace.
func (s *Staged) Swap() error {
	if len(s.staging) == 0 {
		return nil
	}
	rendered := filepath.Join(s.root, ".rendered")
	if err := os.MkdirAll(rendered, 0o755); err != nil {
		return err
	}
	var errs []error
	swapped := map[string]State{}
	for _, name := range slices.Sorted(maps.Keys(s.staging)) {
		if err := s.swap(name, rendered); err != nil {
			errs = append(errs, fmt.Errorf("output %s: swap in the rendered files: %w", name, err))
			continue
		}
		swapped[name] = s.states[name]
	}
	if err := updateStatus(s.root, func(st map[string]Status) {
		for name, state := range swapped {
			st[name] = Status{Hash: state.Files}
		}
	}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Staged) swap(name, rendered string) error {
	def := s.defs[name]
	dir := def.Dir
	info, err := os.Lstat(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	exists := err == nil
	old, _ := os.Readlink(dir)
	if exists && !def.Replace && !replaceable(dir, info, old, rendered) {
		return fmt.Errorf("%s holds another program's files; a swap replaces only an empty directory or a link to an earlier render", dir)
	}
	target := filepath.Join(rendered, filepath.Base(s.staging[name]))
	if err := os.Rename(s.staging[name], target); err != nil {
		return err
	}
	delete(s.staging, name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if exists && info.IsDir() {
		// A directory an earlier image or a tmpfiles rule made, which no rename replaces.
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	link := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".link")
	os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		return err
	}
	if err := os.Rename(link, dir); err != nil {
		os.Remove(link)
		return err
	}
	if old != "" && old != target && filepath.Dir(old) == rendered {
		if err := os.RemoveAll(old); err != nil {
			log.Printf("output %s: remove the files it replaced: %v", name, err)
		}
	}
	return nil
}

// ClearFailures removes the failures the status records for the staged outputs, which passed,
// for a render that swaps none in, as an install in place's; the hash each output serves stays.
func (s *Staged) ClearFailures() error {
	if len(s.states) == 0 {
		return nil
	}
	return updateStatus(s.root, func(st map[string]Status) {
		for name := range s.states {
			if e, ok := st[name]; ok {
				e.Error = ""
				st[name] = e
			}
		}
	})
}

// Discard removes the staged outputs that were not swapped in.
func (s *Staged) Discard() {
	for name, dir := range s.staging {
		os.RemoveAll(dir)
		delete(s.staging, name)
	}
}

// replaceable is whether a swap may replace what dir holds without the output's Replace: an empty
// directory, as a tmpfiles rule makes, or a link to an earlier render below rendered.
func replaceable(dir string, info fs.FileInfo, link, rendered string) bool {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return filepath.Dir(link) == rendered
	case info.IsDir():
		entries, err := os.ReadDir(dir)
		return err == nil && len(entries) == 0
	}
	return false
}
