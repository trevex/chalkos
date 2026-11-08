// Package jsonfile reads the JSON files an image may leave out, such as the facts, effects and
// outputs it declares, and decodes JSON strictly.
package jsonfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
)

// Read decodes the JSON file at path into v; a missing file leaves v as it is.
func Read(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// ReadDefs reads the definitions by name that the JSON file at path declares, as an image declares
// its facts, effects or outputs; a missing file declares none. It refuses them with every error
// check returns, in the order of their names, each naming path, what the definition is and its
// name.
func ReadDefs[T any](path, what string, check func(name string, def T) error) (map[string]T, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]T{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseDefs(path, data, what, check)
}

// ParseDefs is ReadDefs of data, the content of the file at path.
func ParseDefs[T any](path string, data []byte, what string, check func(name string, def T) error) (map[string]T, error) {
	defs := map[string]T{}
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if defs == nil {
		// The file holds null.
		defs = map[string]T{}
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		if err := check(name, defs[name]); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s %s: %w", path, what, name, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return defs, nil
}

// ErrTrailingData is the error of DecodeStrict for data after the JSON value.
var ErrTrailingData = errors.New("data after the JSON value")

// DecodeStrict decodes the JSON value data holds into v, refusing fields v lacks and, with
// ErrTrailingData, anything after the value.
func DecodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrTrailingData
	}
	return nil
}
