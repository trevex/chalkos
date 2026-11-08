// Package outputs runs the programs of an image's store that read a node's config documents:
// renderers and validators, which read the same input and report refusals in the same form.
//
// Each output of /etc/chalkos/outputs.json is a renderer that reads the documents of its input
// kinds as JSON and writes a service's config files into a directory, and an optional check of
// those files. chalkd renders and checks every output into a staging directory before a delivery
// changes the node, and swaps each one in by a symlink rename once the delivery is stored, so a
// service never reads half an output; chalkos-config renders them at boot, since /run is a tmpfs.
//
// Each validator of /etc/chalkos/validators.json is a renderer without files: a program that
// checks the documents of its input kinds for what no schema or change rule of the image sees,
// such as what several documents mean together, or what takes a parser or a tool to tell. chalkd
// runs it as
//
//	<program> validate <name>
//
// in the sandbox renderers run in, after the change rules and before the renderers, in a
// delivery, its dry run, an install in place and the image's check of its own config. A boot runs
// none, as every slot was written only once the checker of its image passed the documents.
// chalkctl runs the validators it links, LinkedValidators that read the same JSON, in-process
// when it checks a node's config.
//
// Every program reads one JSON object on standard input, {"node": {"name": …}, "documents":
// {"<Kind>": [<document>, …]}}: the documents of each of its input kinds, sorted by name, with
// their schema defaults, without the Secrets and with every other secret value hashed. A renderer
// or a validator refuses a config by printing a report of refusals on standard output, as
// pkg/refusals describes it, whatever its exit status; otherwise exit status 0 means that it ran,
// and any other status is a failure of the program, which refuses the config too. What a program
// writes to standard error goes to the node's journal alone.
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
	programs := map[string]string{partRenderer: d.Render}
	if d.Check != "" {
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
