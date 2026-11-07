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
	"os"
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
