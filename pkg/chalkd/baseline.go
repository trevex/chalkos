package chalkd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"slices"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
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
	Facts     recordedFacts            `json:"facts"`
	Outputs   map[string]outputs.State `json:"outputs"`
	// Kinds are the hashes of the kinds' documents, by kind, as config.KindHashes gives them. A
	// baseline an older chalkd wrote has none, which makes every effect on a kind due.
	Kinds map[string]string `json:"kinds"`
	// Attempts are what each delivery since applied, or may have, as it records before its first
	// change: a unit it restarted runs that delivery's config, which a later delivery of the
	// baseline's own config must undo.
	Attempts []baselineAttempt `json:"attempts,omitempty"`
}

// baselineAttempt is what a delivery applied, or may have: its documents, with their secret
// values hashed, the states of the outputs it rendered, the hashes of its kinds and the facts the
// rules read once it changed the node.
type baselineAttempt struct {
	Documents []map[string]any         `json:"documents"`
	Outputs   map[string]outputs.State `json:"outputs"`
	Kinds     map[string]string        `json:"kinds"`
	// Facts are recorded once the delivery loaded them, after its changes. An attempt without
	// them, as one chalkd stopped before, makes every rule that reads a fact due.
	Facts recordedFacts `json:"facts,omitempty"`
}

// baselineFact is a fact as a rule read it: its value, or why it could not be loaded, which says
// nothing of the value.
type baselineFact struct {
	Value any    `json:"value"`
	Error string `json:"error,omitempty"`
}

// recordedFacts are the facts the rules read, by name, as a baseline or an attempt records them.
type recordedFacts map[string]baselineFact

// recordFacts loads the facts names from f.
func recordFacts(names []string, f rules.Facts) recordedFacts {
	r := recordedFacts{}
	for _, name := range names {
		v, err := f.Fact(name)
		if err != nil {
			r[name] = baselineFact{Error: err.Error()}
			continue
		}
		r[name] = baselineFact{Value: v}
	}
	return r
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
	return baseline{Documents: redacted(old), Facts: recordFacts(names, f), Outputs: states, Kinds: kinds}
}

// write records the baseline at path, readable by root alone.
func (b baseline) write(path string) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data, 0o600)
}

// applied is what the attempt applied, its facts left out, as JSON.
func (a baselineAttempt) applied() ([]byte, error) {
	a.Facts = nil
	return json.Marshal(a)
}

// addAttempt adds a, what a delivery applies, to the baseline at path, which that delivery
// compares with. An attempt that applied the same, as a retry of the same delivery leaves it,
// goes, so the baseline holds each once, with the facts of its last run.
func addAttempt(path string, a baselineAttempt) error {
	b, found, err := readBaseline(path)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("the delivery left no baseline")
	}
	data, err := a.applied()
	if err != nil {
		return err
	}
	b.Attempts = slices.DeleteFunc(b.Attempts, func(o baselineAttempt) bool {
		od, err := o.applied()
		return err == nil && bytes.Equal(od, data)
	})
	b.Attempts = append(b.Attempts, a)
	return b.write(path)
}

// setAttemptFacts records facts, as the delivery that applied what a applied loaded them once it
// changed the node, on that attempt in the baseline at path.
func setAttemptFacts(path string, a baselineAttempt, facts recordedFacts) error {
	b, found, err := readBaseline(path)
	if err != nil || !found {
		return err
	}
	data, err := a.applied()
	if err != nil {
		return err
	}
	for i, o := range b.Attempts {
		if od, err := o.applied(); err == nil && bytes.Equal(od, data) {
			b.Attempts[i].Facts = facts
			return b.write(path)
		}
	}
	return nil
}

// documents are the baseline's documents, with their secret values hashed.
func (b baseline) documents() []config.Document {
	return fromObjects(b.Documents)
}

// documents are the attempt's documents, with their secret values hashed.
func (a baselineAttempt) documents() []config.Document {
	return fromObjects(a.Documents)
}

// fromObjects are the documents of a baseline's objects.
func fromObjects(objects []map[string]any) []config.Document {
	docs := make([]config.Document, len(objects))
	for i, obj := range objects {
		docs[i] = config.FromObject(obj)
	}
	return docs
}

// redacted are docs as a baseline records them, with their secret values hashed.
func redacted(docs []config.Document) []map[string]any {
	var objects []map[string]any
	for _, d := range config.Redact(docs) {
		objects = append(objects, d.Object)
	}
	return objects
}

// Fact is the fact name as it was; a fact the rules did not read then, which the running image's
// rules all read, cannot be told, so the rules that read it are due.
func (f recordedFacts) Fact(name string) (any, error) {
	r, ok := f[name]
	switch {
	case !ok:
		return nil, errors.New("not recorded by the delivery that failed")
	case r.Error != "":
		return nil, errors.New(r.Error)
	}
	return r.Value, nil
}

// dropBaseline removes the baseline once a delivery is applied whole. One left behind only makes
// the next delivery compare with an older state, which takes more effects than it needed to.
func (s *Server) dropBaseline() {
	if err := os.Remove(s.Paths.EffectsBaseline); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("remove the baseline of the effects: %v", err)
	}
}
