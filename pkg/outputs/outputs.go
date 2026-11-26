// Package outputs runs the programs of an image's store that read a node's config documents.
// Each output of /etc/chalkos/outputs.json is a renderer that writes a service's config files into
// a directory, with an optional check of them; outputs are staged and swapped in by a symlink
// rename once the delivery is stored, so a service never reads half an output, and rendered again
// at boot, since /run is a tmpfs. Each validator of /etc/chalkos/validators.json is a renderer
// without files, which runs after the change rules and before the renderers, never at boot, as
//
//	<program> validate <name>
//
// Every program reads one JSON object on standard input, {"node": {"name": …}, "documents":
// {"<Kind>": [<document>, …]}}: the documents of its input kinds, sorted by name, with their
// defaults, without the Secrets and with every other secret value hashed. A program refuses a
// config by printing a pkg/refusals report on standard output, whatever its exit status; otherwise
// any status but 0 is a failure, which refuses the config too. Standard error goes to the journal.
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
	// DefaultRoot is where a node renders the outputs: each output's files below
	// DefaultRoot/.rendered, linked from the output's dir.
	DefaultRoot = "/run/chalkos/outputs"
	// MaxSize bounds the bytes of an output's files, which land on /run, a tmpfs.
	MaxSize = 16 << 20
)

// Definition is an output as outputs.json declares it.
type Definition struct {
	// Inputs are the kinds, by name, whose documents the renderer reads.
	Inputs []string `json:"inputs"`
	// Render is a program of the image's store and its arguments, which writes the output's
	// files into $OUT; Check, optional, a program and its arguments that reads them there and
	// fails to refuse them.
	Render []string `json:"render"`
	Check  []string `json:"check"`
	// Dir is where services read the files: a link to the directory that holds them.
	Dir string `json:"dir"`
	// User, Group and Mode are the owner and the octal mode of the files.
	User  string `json:"user"`
	Group string `json:"group"`
	Mode  string `json:"mode"`
	// Replace lets a swap replace whatever dir holds, as the networkd output replaces what else
	// writes to /run/systemd/network; without it a swap replaces only nothing, an empty
	// directory or a link to an earlier render.
	Replace bool `json:"replace,omitempty"`
}

// Read reads the outputs an image declares for a node that renders them below root, DefaultRoot
// on a node; a missing file declares none.
func Read(path, root string) (map[string]Definition, error) {
	defs, err := jsonfile.ReadDefs(path, string(RoleOutput), func(name string, d Definition) error { return d.check(name, root) })
	if err != nil {
		return nil, err
	}
	var errs []error
	names := slices.Sorted(maps.Keys(defs))
	for _, name := range names {
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

// check refuses a definition of the output name chalkd could not carry out: one checkDefinition
// refuses, a dir it must not replace or a mode that is no octal number.
func (d Definition) check(name, root string) error {
	programs := map[string][]string{partRenderer: d.Render}
	if len(d.Check) > 0 {
		programs[partCheck] = d.Check
	}
	if err := checkDefinition(name, d.Inputs, programs); err != nil {
		return err
	}
	if err := checkDir(name, d.Dir, root); err != nil {
		return err
	}
	if _, err := d.mode(); err != nil {
		return err
	}
	return nil
}

// runOwned are the directories below /run that systemd, NixOS and chalkd keep their state in, which
// no output's dir may be or lie in, but for networkd's dir, /run/systemd/network.
var runOwned = []string{"systemd", "credentials", "chalkd", "wrappers", "current-system", "booted-system", "user", "dbus", "udev", "keys", "lock", "log"}

// checkDir refuses a dir of the output name that a swap could not safely replace by a link: one
// that is not clean or lies outside /run, a tmpfs, one with a dot component like chalkd's own
// links, one in a directory of runOwned, or one in /run/chalkos, where the config and credentials
// live, other than below root/<name>.
func checkDir(name, dir, root string) error {
	chalkos := filepath.Dir(root)
	run := filepath.Dir(chalkos)
	rest, ok := strings.CutPrefix(dir, run+"/")
	if !ok || filepath.Clean(dir) != dir || slices.ContainsFunc(strings.Split(rest, "/"), func(c string) bool { return strings.HasPrefix(c, ".") }) {
		return fmt.Errorf("the dir %q must be a clean path below %s with no component that starts with a dot", dir, run)
	}
	if top, _, _ := strings.Cut(rest, "/"); slices.Contains(runOwned, top) && dir != run+"/systemd/network" {
		return fmt.Errorf("the dir %q takes %s/%s or a path below it, which systemd, NixOS or chalkd own", dir, run, top)
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
