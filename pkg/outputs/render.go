package outputs

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Renderer runs renderers, their checks and validators below Root.
type Renderer struct {
	Root string
	Run  Runner
	// Timeout bounds each program; zero is DefaultTimeout.
	Timeout time.Duration
	// KeepOwner leaves the files the renderer's, as the image's build does, which has none of the
	// image's users.
	KeepOwner bool
}

// Default renders on the node, in the sandbox.
func Default() Renderer {
	return Renderer{Root: DefaultRoot, Run: Sandbox{}}
}

// Render renders and checks every output of defs from in, each in a directory of its own below
// Root/.staging, and returns those that passed and the failures of the others. It changes nothing
// a service reads; what an earlier render left staged is removed first.
func (r Renderer) Render(ctx context.Context, defs map[string]Definition, in Input) (*Staged, []Failure) {
	s := &Staged{root: r.Root, defs: defs, staging: map[string]string{}, states: map[string]State{}}
	if len(defs) == 0 {
		return s, nil
	}
	staging := filepath.Join(r.Root, ".staging")
	var failures []Failure
	if err := os.RemoveAll(staging); err != nil {
		for _, name := range slices.Sorted(maps.Keys(defs)) {
			failures = append(failures, failure(RoleOutput, name, err))
		}
		return s, failures
	}
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		dir, state, err := r.render(ctx, name, defs[name], in, staging)
		if err != nil {
			if dir != "" {
				os.RemoveAll(dir)
			}
			failures = append(failures, failure(RoleOutput, name, err))
			continue
		}
		s.staging[name], s.states[name] = dir, state
	}
	return s, failures
}

func (r Renderer) render(ctx context.Context, name string, d Definition, in Input, staging string) (string, State, error) {
	dir, err := scratch(staging, name)
	if err != nil {
		return "", State{}, err
	}
	stdin, err := in.stdin(d.Inputs)
	if err != nil {
		return dir, State{}, err
	}
	// The renderer writes $OUT and reports refusals.
	if err := r.run(ctx, RoleOutput, name, partRenderer, Program{Argv: strings.Fields(d.Render), Stdin: stdin, Out: dir, Write: true, Report: true}); err != nil {
		return dir, State{}, err
	}
	if err := checkFiles(dir); err != nil {
		return dir, State{}, err
	}
	// The check reads the files as the renderer wrote them, which the output's owner and mode may
	// keep from the sandbox's user, and may open netlink sockets, as nft --check needs.
	if d.Check != "" {
		if err := r.run(ctx, RoleOutput, name, partCheck, Program{Argv: strings.Fields(d.Check), Stdin: stdin, Out: dir, Netlink: true}); err != nil {
			return dir, State{}, err
		}
	}
	files, err := hashDir(dir)
	if err != nil {
		return dir, State{}, err
	}
	if err := own(dir, d, r.KeepOwner); err != nil {
		return dir, State{}, err
	}
	return dir, State{Files: files, Secrets: d.secrets(in)}, nil
}

// checkFiles refuses an output that holds anything but directories and regular files, such as a
// link chalkd would follow when it sets the owner, or more than MaxSize bytes.
func checkFiles(dir string) error {
	var size int64
	return filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case e.IsDir():
			return nil
		case !e.Type().IsRegular():
			return fmt.Errorf("its renderer wrote %s, which is no regular file", rel)
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if size += info.Size(); size > MaxSize {
			return fmt.Errorf("its renderer wrote more than %d bytes", MaxSize)
		}
		return nil
	})
}

// own gives the output's files its owner and mode, and its directories the owner and 0755; with
// keep set, the modes alone.
func own(dir string, d Definition, keep bool) error {
	mode, err := d.mode()
	if err != nil {
		return err
	}
	uid, gid := -1, -1
	if !keep {
		if uid, err = lookup(d.User, func(n string) (string, error) {
			u, err := user.Lookup(n)
			if err != nil {
				return "", err
			}
			return u.Uid, nil
		}); err != nil {
			return fmt.Errorf("its user %s: %w", d.User, err)
		}
		if gid, err = lookup(d.Group, func(n string) (string, error) {
			g, err := user.LookupGroup(n)
			if err != nil {
				return "", err
			}
			return g.Gid, nil
		}); err != nil {
			return fmt.Errorf("its group %s: %w", d.Group, err)
		}
	}
	return filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !keep {
			if err := os.Lchown(path, uid, gid); err != nil {
				return err
			}
		}
		if e.IsDir() {
			return os.Chmod(path, 0o755)
		}
		return os.Chmod(path, mode)
	})
}

// lookup is the numeric ID of name, which is a number already or a name find resolves.
func lookup(name string, find func(string) (string, error)) (int, error) {
	if id, err := strconv.Atoi(name); err == nil {
		return id, nil
	}
	id, err := find(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(id)
}
