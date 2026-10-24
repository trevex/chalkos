package outputs

import (
	"errors"
	"fmt"
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

// Swap makes each staged output the one its dir links to: the files move below Root/.rendered,
// and a new link replaces the old one in one rename, so a service reads the old files or the new
// ones and never a mix. The files the old link named are removed. It records each swapped
// output's hash in the status.
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
	dir := s.defs[name].Dir
	target := filepath.Join(rendered, filepath.Base(s.staging[name]))
	if err := os.Rename(s.staging[name], target); err != nil {
		return err
	}
	delete(s.staging, name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	old, _ := os.Readlink(dir)
	if info, err := os.Lstat(dir); err == nil && info.IsDir() {
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
