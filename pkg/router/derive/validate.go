package derive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/trevex/chalkos/pkg/refusals"
)

// Validate is the validator router: chalkd runs it as chalkos-render validate router on the
// documents of the kinds of Kinds that the image declares, and chalkctl links it and runs it
// in-process on the same documents, so both refuse a config alike. documents is what a validator
// reads on standard input, a JSON array of documents with their schema defaults and their secret
// values hashed. Validate reads them as Build takes them and refuses nothing: the router's
// renderers refuse what the model refuses. An input that is no array of documents is its error,
// the validator's failure.
func Validate(documents []byte) ([]refusals.Refusal, error) {
	if _, err := readDocuments(documents); err != nil {
		return nil, err
	}
	return nil, nil
}

// readDocuments is data, a JSON array of documents, as Build takes it: by kind, each kind's
// documents in the array's order, which sorts them by name, and the numbers exact.
func readDocuments(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var docs []map[string]any
	if err := dec.Decode(&docs); err != nil {
		return nil, fmt.Errorf("the documents are no JSON array of documents: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, errors.New("the documents are no JSON array of documents: more follows the array")
	}
	out := map[string]any{}
	for i, doc := range docs {
		kind, _ := doc["kind"].(string)
		if kind == "" {
			return nil, fmt.Errorf("document %d names no kind", i+1)
		}
		list, _ := out[kind].([]any)
		out[kind] = append(list, any(doc))
	}
	return out, nil
}
