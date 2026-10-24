// Package effects reads what an image declares about its services: services.json, the units
// granted config kinds, and effects.json, what a delivery does to those units and to chalkd.
// chalkd reads both on the node; chalkos-rules checks effects.json when the image is built.
package effects

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/command"
)

// Service is a unit granted config kinds, as /etc/chalkos/services.json records it: chalkd
// writes the documents of its kinds below /run/chalkos/config and the values their secretRefs
// name into the unit's credentials. A grant never restarts the unit; an effect does.
type Service struct {
	Kinds []string `json:"kinds"`
}

// ChalkdAction is what an effect does to chalkd's own loops.
type ChalkdAction string

// The values of Definition.Chalkd.
const (
	ChalkdRestart ChalkdAction = "restart"
	ChalkdReload  ChalkdAction = "reload"
)

// Definition is what a delivery does to the node's services, as /etc/chalkos/effects.json records
// it: when one of the outputs Outputs changed, the documents of one of the kinds Kinds did, or the
// CEL rule When holds, chalkd reloads the units of Reload, runs Run, restarts the units of
// Restart, starting them too with Start, and acts on its own loops as Chalkd says.
type Definition struct {
	Outputs []string     `json:"outputs"`
	Kinds   []string     `json:"kinds"`
	When    string       `json:"when"`
	Restart []string     `json:"restart"`
	Reload  []string     `json:"reload"`
	Run     string       `json:"run"`
	Start   bool         `json:"start"`
	Chalkd  ChalkdAction `json:"chalkd"`
}

// ReadServices reads the image's services.json; an image without one grants no unit a kind.
func ReadServices(path string) (map[string]Service, error) {
	services := map[string]Service{}
	if err := readJSON(path, &services); err != nil {
		return nil, err
	}
	return services, nil
}

// Read reads the image's effects.json, refusing an effect chalkd could not carry out; an image
// without one has no effects.
func Read(path string) (map[string]Definition, error) {
	effects := map[string]Definition{}
	if err := readJSON(path, &effects); err != nil {
		return nil, err
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(effects)) {
		if err := effects[name].Check(); err != nil {
			errs = append(errs, fmt.Errorf("%s: effect %s: %w", path, name, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return effects, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// Check refuses an effect that nothing triggers or that does nothing, a unit that is no unit
// name, a value of Chalkd chalkd does not know, and a command that names no program: chalkd
// splits Run on spaces, with no shell quoting, and runs the first word, a program of its PATH or
// of the image's store.
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
	if d.Chalkd != "" && d.Chalkd != ChalkdRestart && d.Chalkd != ChalkdReload {
		return fmt.Errorf("chalkd is %q, not %s or %s", d.Chalkd, ChalkdRestart, ChalkdReload)
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
