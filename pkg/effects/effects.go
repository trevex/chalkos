// Package effects reads what an image declares about its services: services.json, the units
// granted config kinds, and effects.json, what a delivery does to those units and to chalkd.
// chalkd reads both on the node; chalkos-rules checks effects.json when the image is built.
package effects

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/command"
	"github.com/trevex/chalkos/pkg/jsonfile"
)

// Service is a unit granted config kinds, as /etc/chalkos/services.json records it: chalkd
// writes the documents of its kinds below /run/chalkos/config and the values their secretRefs
// name into the unit's credentials. A grant never restarts the unit; an effect does.
type Service struct {
	Kinds []string `json:"kinds"`
}

// Hint says why an effect's units may fail to restart: Text, followed by the content of File
// while it holds text.
type Hint struct {
	Text string `json:"text"`
	File string `json:"file"`
}

// Definition is what a delivery does to the node's services, as /etc/chalkos/effects.json records
// it: when one of the outputs Outputs changed, the documents of one of the kinds Kinds did, or the
// CEL rule When holds, chalkd reloads the units of Reload, runs Run, restarts the units of
// Restart, starting them too with Start, and runs the layer action Chalkd names on what only the
// running chalkd holds.
type Definition struct {
	Outputs []string `json:"outputs"`
	Kinds   []string `json:"kinds"`
	When    string   `json:"when"`
	Restart []string `json:"restart"`
	Reload  []string `json:"reload"`
	Run     string   `json:"run"`
	Start   bool     `json:"start"`
	// Chalkd names a layer's action, <layer>:<action>; "" for none.
	Chalkd string `json:"chalkd"`
	Hint   *Hint  `json:"hint"`
}

// ReadServices reads the image's services.json; an image without one grants no unit a kind.
func ReadServices(path string) (map[string]Service, error) {
	services := map[string]Service{}
	if err := jsonfile.Read(path, &services); err != nil {
		return nil, err
	}
	return services, nil
}

// Read reads the image's effects.json, refusing an effect chalkd could not carry out and two
// effects that give one unit different hints, since chalkd looks a hint up by its unit; an image
// without one has no effects.
func Read(path string) (map[string]Definition, error) {
	effects := map[string]Definition{}
	if err := jsonfile.Read(path, &effects); err != nil {
		return nil, err
	}
	var errs []error
	// hinted is the effect that first gives each unit a hint, by unit.
	hinted := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(effects)) {
		d := effects[name]
		if err := d.Check(); err != nil {
			errs = append(errs, fmt.Errorf("%s: effect %s: %w", path, name, err))
			continue
		}
		if d.Hint == nil {
			continue
		}
		for _, unit := range d.Restart {
			first, ok := hinted[unit]
			switch {
			case !ok:
				hinted[unit] = name
			case *effects[first].Hint != *d.Hint:
				errs = append(errs, fmt.Errorf("%s: effects %s and %s give %s different hints", path, first, name, unit))
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return effects, nil
}

// layerAction is what Chalkd names: <layer>:<action>.
var layerAction = regexp.MustCompile(`^[a-z][a-z0-9-]*:[a-z][a-z0-9-]*$`)

// Check refuses an effect that nothing triggers or that does nothing, a unit that is no unit
// name, a Chalkd that names no layer's action, a hint without text or with a relative file, and
// a command that names no program: chalkd splits Run on spaces, with no shell quoting, and runs
// the first word, a program of its PATH or of the image's store.
func (d Definition) Check() error {
	if len(d.Outputs) == 0 && len(d.Kinds) == 0 && d.When == "" {
		return errors.New("it has none of outputs, kinds and when, so nothing triggers it")
	}
	if len(d.Restart) == 0 && len(d.Reload) == 0 && d.Run == "" && d.Chalkd == "" {
		return errors.New("it restarts, reloads and runs nothing")
	}
	for _, unit := range slices.Concat(d.Restart, d.Reload) {
		if unit == "" || strings.ContainsAny(unit, " /") || !strings.Contains(unit, ".") {
			return fmt.Errorf("%q is no unit name, such as bird.service", unit)
		}
	}
	if d.Chalkd != "" && !layerAction.MatchString(d.Chalkd) {
		return fmt.Errorf("chalkd is %q, not <layer>:<action>", d.Chalkd)
	}
	if d.Hint != nil {
		switch {
		case d.Hint.Text == "":
			return errors.New("its hint has no text")
		case d.Hint.File != "" && !filepath.IsAbs(d.Hint.File):
			return fmt.Errorf("its hint's file %q is no absolute path", d.Hint.File)
		}
	}
	if d.Run != "" {
		argv := strings.Fields(d.Run)
		switch {
		case len(argv) == 0:
			return errors.New("the command to run is empty")
		case strings.Contains(argv[0], "/") && !command.InStore(argv[0]):
			return fmt.Errorf("the command %q names %s, which is neither a program of the image's store nor a bare program name", d.Run, argv[0])
		}
	}
	return nil
}
