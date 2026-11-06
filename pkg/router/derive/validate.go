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
// values hashed. Validate reads them as Build takes them and returns every refusal of the checks
// that check runs, each once, with the runes of their messages that refusals.Unprintable reports
// as spaces. An input that is no array of documents, and a guard's internal error, are its
// error, the validator's failure: chalkd and chalkctl run the validator after the schemas, the
// references and the rules have checked the documents, which keeps every guard from firing.
func Validate(documents []byte) ([]refusals.Refusal, error) {
	byKind, err := readDocuments(documents)
	if err != nil {
		return nil, err
	}
	rs, others := refusals.Split(check(byKind))
	if len(others) > 0 {
		return nil, errors.Join(others...)
	}
	for i := range rs {
		rs[i].Message = refusals.Printable(rs[i].Message)
	}
	return rs, nil
}

// check runs the router's checks of what the documents of byKind mean together, which no schema
// or rule sees, and joins their refusals: the VRFs' links as Build checks them and the NAT64s as
// NAT64Instances checks them. Each goes on past a refusal to the next document, so a config with
// several faults gets a refusal for each. A check refuses what the outputs that apply its
// documents would apply otherwise than the documents say, so it runs whether or not the image
// renders those outputs: an image without a feature declares none of its kinds, whose documents
// the validator then never reads.
func check(byKind map[string]any) error {
	d, err := Build(byKind)
	_, nerr := d.NAT64Instances()
	return errors.Join(err, nerr)
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
