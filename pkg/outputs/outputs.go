// Package outputs renders a service's config files from the node's config documents: each output
// of /etc/chalkos/outputs.json is a store program that reads the documents of its input kinds as
// JSON and writes files into a directory, and an optional check of those files. chalkd renders
// and checks every output into a staging directory before a delivery changes the node, and swaps
// each one in by a symlink rename once the delivery is stored, so a service never reads half an
// output; chalkos-config renders them at boot, since /run is a tmpfs.
package outputs

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/jsonfile"
)

const (
	// DefaultPath is where an image declares its outputs, and DefaultRoot where a node renders
	// them: each output's files below DefaultRoot/.rendered, linked from the output's dir.
	DefaultPath = "/etc/chalkos/outputs.json"
	DefaultRoot = "/run/chalkos/outputs"
	// MaxSize bounds the bytes of an output's files, which land on /run, a tmpfs.
	MaxSize = 16 << 20
)

// Definition is an output as outputs.json declares it.
type Definition struct {
	// Inputs are the kinds, by name, whose documents the renderer reads.
	Inputs []string `json:"inputs"`
	// Render is a program of the image's store with its arguments, which writes the output's
	// files into $OUT; Check, optional, a program that reads them there and fails to refuse them.
	Render string `json:"render"`
	Check  string `json:"check,omitempty"`
	// Dir is where services read the files: a link to the directory that holds them.
	Dir string `json:"dir"`
	// User, Group and Mode are the owner and the octal mode of the files.
	User  string `json:"user"`
	Group string `json:"group"`
	Mode  string `json:"mode"`
}

// Read reads the outputs an image declares for a node that renders them below root, DefaultRoot
// on a node; a missing file declares none.
func Read(path, root string) (map[string]Definition, error) {
	defs := map[string]Definition{}
	if err := jsonfile.Read(path, &defs); err != nil {
		return nil, err
	}
	var errs []error
	names := slices.Sorted(maps.Keys(defs))
	for _, name := range names {
		if err := defs[name].check(name, root); err != nil {
			errs = append(errs, fmt.Errorf("%s: output %s: %w", path, name, err))
		}
		for _, other := range names {
			dir, in := defs[name].Dir, defs[other].Dir
			switch {
			case other < name && dir == in:
				errs = append(errs, fmt.Errorf("%s: output %s: the dir %q is the dir of the output %s", path, name, dir, other))
			case strings.HasPrefix(dir, in+"/"):
				errs = append(errs, fmt.Errorf("%s: output %s: the dir %q is inside the dir of the output %s", path, name, dir, other))
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return defs, nil
}

// check refuses a definition of the output name chalkd could not carry out: a program that is no
// absolute path, a dir it must not replace or a mode that is no octal number. The image's build
// checks that the programs are of its store.
func (d Definition) check(name, root string) error {
	for _, p := range []struct {
		role    role
		command string
	}{{roleRenderer, d.Render}, {roleCheck, d.Check}} {
		if p.role == roleCheck && p.command == "" {
			continue
		}
		if argv := strings.Fields(p.command); len(argv) == 0 || !filepath.IsAbs(argv[0]) {
			return fmt.Errorf("the %s %q names no program by its absolute path", p.role, p.command)
		}
	}
	if err := checkDir(name, d.Dir, root); err != nil {
		return err
	}
	if _, err := d.mode(); err != nil {
		return err
	}
	return nil
}

// checkDir refuses a dir of the output name that a swap could not safely replace by a link. root
// is /run/chalkos/outputs. It refuses a dir that:
//   - lies outside /run, a tmpfs, or is not clean;
//   - has a component that starts with a dot, as the links and directories chalkd keeps beside
//     the outputs' do;
//   - lies in /run/chalkos, where chalkd keeps the node's config and credentials, other than
//     /run/chalkos/outputs/<name> or a path below it.
func checkDir(name, dir, root string) error {
	chalkos := filepath.Dir(root)
	run := filepath.Dir(chalkos)
	rest, ok := strings.CutPrefix(dir, run+"/")
	if !ok || filepath.Clean(dir) != dir || slices.ContainsFunc(strings.Split(rest, "/"), func(c string) bool { return strings.HasPrefix(c, ".") }) {
		return fmt.Errorf("the dir %q must be a clean path below %s with no component that starts with a dot", dir, run)
	}
	own := filepath.Join(root, name)
	if (dir == chalkos || strings.HasPrefix(dir, chalkos+"/")) && dir != own && !strings.HasPrefix(dir, own+"/") {
		return fmt.Errorf("the dir %q is in %s, where an output may take only %s", dir, chalkos, own)
	}
	return nil
}

func (d Definition) mode() (fs.FileMode, error) {
	m, err := strconv.ParseUint(d.Mode, 8, 32)
	if err != nil || m > 0o777 {
		return 0, fmt.Errorf("the mode %q is no octal file mode", d.Mode)
	}
	return fs.FileMode(m), nil
}
