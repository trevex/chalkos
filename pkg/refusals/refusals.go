// Package refusals is the format in which a program that checks a node's documents says what it
// refuses, for what no schema or rule of the image sees: a renderer of an output, and a
// validator. The program prints one JSON object on standard output,
//
//	{"refusals": [{"kind": "BGPPeer", "name": "fabric", "path": "spec.interfaces", "message": "..."}]}
//
// each refusal naming a document by its kind and name, the field it is about by its path in the
// document as written, such as spec.rules[0].source.set, and what is wrong and how to fix it.
// chalkd reports refusals to the operator as "<Kind>/<name>: <path>: <message>", as it reports
// a reference that does not resolve, after "the config is refused: ", as it reports a rule's.
// What else the program writes, to standard error, goes to the node's journal alone, since it can
// echo the documents.
//
// A refusal cannot carry a secret's value: the program reads the documents without the Secrets
// and with every other secret value hashed, as rules read them.
package refusals

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// A Refusal is a document's field that a program refuses, and why.
type Refusal struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// String is the refusal as chalkd reports it: "<Kind>/<name>: <path>: <message>".
func (r Refusal) String() string {
	return r.Kind + "/" + r.Name + ": " + r.Path + ": " + r.Message
}

var (
	// kind is a kind's name, as a kind's schema declares it.
	kind = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	// name is a document's name, as every kind's schema holds metadata.name to it.
	name = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	// path is a field's path: names, dots and list indices, without spaces or colons, so that the
	// reported line reads unambiguously.
	path = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+|\[[0-9]+\])*$`)
)

// Unprintable reports whether c would break the reported line, act on the operator's terminal or
// reorder the text around it: a C0 or C1 control character, a line break and CSI among them, DEL,
// and a bidirectional control such as U+202E.
func Unprintable(c rune) bool {
	return unicode.IsControl(c) || unicode.Is(unicode.Bidi_Control, c)
}

// Printable is s with each rune Unprintable reports replaced by a space.
func Printable(s string) string {
	return strings.Map(func(c rune) rune {
		if Unprintable(c) {
			return ' '
		}
		return c
	}, s)
}

// Check refuses a refusal outside the format: a kind, a document name or a path that is none,
// and an empty message or one with a rune Unprintable reports.
func (r Refusal) Check() error {
	switch {
	case !kind.MatchString(r.Kind):
		return fmt.Errorf("kind %q is no kind", r.Kind)
	case !name.MatchString(r.Name):
		return fmt.Errorf("name %q is no document name", r.Name)
	case !path.MatchString(r.Path):
		return fmt.Errorf("path %q is no field's path", r.Path)
	case strings.TrimSpace(r.Message) == "":
		return errors.New("the message is empty")
	case strings.ContainsFunc(r.Message, Unprintable):
		return errors.New("the message holds a control character")
	}
	return nil
}

// Report is what a program prints: its refusals, none for a config it accepts.
type Report struct {
	Refusals []Refusal `json:"refusals"`
}

// MaxReport bounds what a program may print, so that it cannot make a delivery's refusal large.
const MaxReport = 64 << 10

// Parse reads what a program printed: nothing, or one Report and nothing after it. It returns
// the refusals each once, in the order printed. A report outside the format, null, a report
// without its list of refusals or with null, a field it does not know or spells in other case, a
// refusal Check refuses or more than MaxReport bytes, is an error of the program, which says what
// is wrong and quotes no more than the kind, name or path Check refuses.
func Parse(data []byte) ([]Refusal, error) {
	if len(data) > MaxReport {
		return nil, fmt.Errorf("it holds more than %d bytes", MaxReport)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, errors.New("it is no report of refusals")
	}
	if len(bytes.TrimSpace(data[dec.InputOffset():])) != 0 {
		return nil, errors.New("it holds more after its report of refusals")
	}
	rep, ok := fields(raw, "refusals")
	var items []json.RawMessage
	if !ok || json.Unmarshal(rep["refusals"], &items) != nil || items == nil {
		return nil, errors.New("it is no report of refusals")
	}
	var out []Refusal
	for i, item := range items {
		r, err := parseRefusal(item)
		if err == nil {
			err = r.Check()
		}
		if err != nil {
			return nil, fmt.Errorf("refusal %d: %w", i+1, err)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// parseRefusal reads a refusal of a report: an object of the four string fields, each spelled as
// Refusal's tags spell it.
func parseRefusal(raw json.RawMessage) (Refusal, error) {
	var r Refusal
	f, ok := fields(raw, "kind", "name", "path", "message")
	for _, x := range []struct {
		key string
		to  *string
	}{{"kind", &r.Kind}, {"name", &r.Name}, {"path", &r.Path}, {"message", &r.Message}} {
		if ok && json.Unmarshal(f[x.key], x.to) != nil {
			ok = false
		}
	}
	if !ok {
		return Refusal{}, errors.New("it is no refusal")
	}
	return r, nil
}

// fields is the JSON object raw by its keys, when it holds exactly the keys given, compared by
// case, unlike encoding/json's decoding into a struct; a key given twice is refused as well.
func fields(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		key, isString := t.(string)
		if err != nil || !isString || !slices.Contains(keys, key) {
			return nil, false
		}
		if _, dup := out[key]; dup {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		out[key] = v
	}
	return out, len(out) == len(keys)
}

// Encode is the report of rs as a program prints it, on one line.
func Encode(rs []Refusal) ([]byte, error) {
	if rs == nil {
		rs = []Refusal{}
	}
	data, err := json.Marshal(Report{Refusals: rs})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Message is how chalkd refuses a config with rs, which several programs may have reported:
// "the config is refused: " and each refusal once, as String gives it, joined by "; ".
func Message(rs []Refusal) string {
	var lines []string
	for _, r := range rs {
		if s := r.String(); !slices.Contains(lines, s) {
			lines = append(lines, s)
		}
	}
	return "the config is refused: " + strings.Join(lines, "; ")
}
