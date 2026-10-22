package chalkd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
)

// baseline is what the effects compare a delivery with while an earlier one is not applied
// whole: the running slot's config, the facts the rules read, the states of the outputs and
// the hashes of the kinds, as they were before the first delivery that failed part way. What that
// delivery applied, such as a share or a slot, is then no reason for a retry to find nothing
// changed. It holds what the rules see alone: the documents with their secret values hashed, as
// config.Redact hashes them, and the facts as their definitions keep them, the share's keys among
// them as hashes. A reboot, which clears /run, starts every unit anyway.
type baseline struct {
	Documents []map[string]any         `json:"documents"`
	Facts     map[string]baselineFact  `json:"facts"`
	Outputs   map[string]outputs.State `json:"outputs"`
	// Kinds are the hashes of the kinds' documents, by kind, as config.KindHashes gives them. A
	// baseline an older chalkd wrote has none, which makes every effect on a kind due.
	Kinds map[string]string `json:"kinds"`
}

// baselineFact is a fact as a rule read it: its value, or why it could not be loaded, which says
// nothing of the value.
type baselineFact struct {
	Value any    `json:"value"`
	Error string `json:"error,omitempty"`
}

// readBaseline reads the baseline at path; found is false when there is none.
func readBaseline(path string) (b baseline, found bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return baseline{}, false, nil
	}
	if err != nil {
		return baseline{}, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// Numbers stay json.Number, as documents and facts hold them.
	dec.UseNumber()
	if err := dec.Decode(&b); err != nil {
		return baseline{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return b, true, nil
}

// newBaseline records old, the running slot's config, the facts the rules read, names, as f
// loads them, the states of the outputs the node serves and the hashes of old's kinds.
func newBaseline(old []config.Document, names []string, f rules.Facts, states map[string]outputs.State, kinds map[string]string) baseline {
	b := baseline{Facts: map[string]baselineFact{}, Outputs: states, Kinds: kinds}
	for _, d := range config.Redact(old) {
		b.Documents = append(b.Documents, d.Object)
	}
	for _, name := range names {
		v, err := f.Fact(name)
		if err != nil {
			b.Facts[name] = baselineFact{Error: err.Error()}
			continue
		}
		b.Facts[name] = baselineFact{Value: v}
	}
	return b
}

// write records the baseline at path, readable by root alone.
func (b baseline) write(path string) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return install.WriteFile(path, data, 0o600)
}

// documents are the baseline's documents, with their secret values hashed.
func (b baseline) documents() []config.Document {
	docs := make([]config.Document, len(b.Documents))
	for i, obj := range b.Documents {
		docs[i] = config.FromObject(obj)
	}
	return docs
}

// Fact is the fact name as it was; a fact the rules did not read then, which the running image's
// rules all read, cannot be told, so the rules that read it are due.
func (b baseline) Fact(name string) (any, error) {
	f, ok := b.Facts[name]
	switch {
	case !ok:
		return nil, errors.New("not recorded before the delivery that failed")
	case f.Error != "":
		return nil, errors.New(f.Error)
	}
	return f.Value, nil
}

// dropBaseline removes the baseline once a delivery is applied whole. One left behind only makes
// the next delivery compare with an older state, which takes more effects than it needed to.
func (s *Server) dropBaseline() {
	if err := os.Remove(s.Paths.EffectsBaseline); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("remove the baseline of the effects: %v", err)
	}
}
