package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/schema"
)

// uniqueDecl is one declaration of a field's or a kind's x-chalkos-unique, as the converter writes
// it from lib.chalkos.mkOption's or chalkos.kinds.<Kind>'s unique: the values that must not repeat
// within one list (across "list") or among all documents of the kind (across "kind"). Without By
// the key is the value itself, or each item of a list, normalised with As; with By it is the tuple
// of those fields of the object, or of each object of a list, each normalised with its own As.
type uniqueDecl struct {
	Across string     `json:"across"`
	By     []keyField `json:"by"`
	As     string     `json:"as"`
	Hint   string     `json:"hint"`
}

// keyField is a member of a declaration's tuple: a field of the object and its normaliser.
type keyField struct {
	Field string `json:"field"`
	As    string `json:"as"`
}

// normalisers turn a string into the form it compares in, and refuse a string they cannot read.
// ip and cidr compare as the rules' ip() and cidr() do, and host as RegistriesOf compares
// registry hosts.
var normalisers = map[string]func(string) (string, bool){
	"ip": func(s string) (string, bool) {
		a, err := netip.ParseAddr(s)
		return a.String(), err == nil
	},
	"cidr": func(s string) (string, bool) {
		p, err := netip.ParsePrefix(s)
		return p.Masked().String(), err == nil
	},
	"host": func(s string) (string, bool) {
		h, err := RegistryHost(s)
		return h, err == nil
	},
	"lower": func(s string) (string, bool) { return strings.ToLower(s), true },
}

// decodeUnique reads a schema node's x-chalkos-unique, refusing keys a declaration does not have,
// a scope other than kind and list, and a normaliser chalkd does not know.
func decodeUnique(v any) ([]uniqueDecl, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var decls []uniqueDecl
	if err := dec.Decode(&decls); err != nil {
		return nil, err
	}
	for _, d := range decls {
		if d.Across != "kind" && d.Across != "list" {
			return nil, fmt.Errorf("across %q is neither kind nor list", d.Across)
		}
		as := []string{d.As}
		for _, f := range d.By {
			as = append(as, f.As)
		}
		for _, a := range as {
			if _, ok := normalisers[a]; a != "" && !ok {
				return nil, fmt.Errorf("the normaliser %q is none of cidr, host, ip and lower", a)
			}
		}
	}
	return decls, nil
}

// keyOf is the form v compares in: normalised with as, which takes strings alone, and a number
// as the exact rational it is, so that 100 and 1e2 compare equal. ok is false for a value the
// normaliser cannot read.
func keyOf(v any, as string) (key any, ok bool) {
	if v == nil {
		return nil, true
	}
	if as != "" {
		s, isString := v.(string)
		if !isString {
			return nil, false
		}
		return normalisers[as](s)
	}
	if n, isNumber := v.(json.Number); isNumber {
		if r, ok := new(big.Rat).SetString(string(n)); ok {
			return json.Number(r.RatString()), true
		}
	}
	return v, true
}

// occurrence is a key where a document holds it: the paths of its members and their values as
// the document writes them.
type occurrence struct {
	doc    ID
	paths  []string
	values []any
}

// label is the occurrence's path, or the tuple of its members' paths.
func (o occurrence) label() string {
	if len(o.paths) == 1 {
		return o.paths[0]
	}
	return "(" + strings.Join(o.paths, ", ") + ")"
}

// shown is the occurrence's value, or the tuple of its members' values, as a message prints it:
// a string quoted, anything else as JSON.
func (o occurrence) shown() string {
	var vs []string
	for _, v := range o.values {
		switch v := v.(type) {
		case string:
			vs = append(vs, strconv.Quote(v))
		case json.Number:
			vs = append(vs, string(v))
		default:
			data, _ := json.Marshal(v)
			vs = append(vs, string(data))
		}
	}
	if len(vs) == 1 {
		return vs[0]
	}
	return "(" + strings.Join(vs, ", ") + ")"
}

// occurrences are the keys of the node n holds under decl, with the encoding each compares by:
// the node's value, or each item of a list. A key whose members are all null or left out takes
// no part, nor does one with a value its normaliser cannot read, which the schema or a rule
// refuses already; a null member of a tuple compares equal to another null.
func occurrences(doc ID, n node, decl uniqueDecl) (keys []string, occs []occurrence) {
	type at struct {
		v    any
		path string
	}
	var values []at
	if items, ok := n.v.([]any); ok {
		for i, item := range items {
			values = append(values, at{item, n.path + "[" + strconv.Itoa(i) + "]"})
		}
	} else {
		values = append(values, at{n.v, n.path})
	}
	for _, x := range values {
		o := occurrence{doc: doc}
		var key []any
		usable, given := true, false
		if len(decl.By) == 0 {
			k, ok := keyOf(x.v, decl.As)
			o.paths, o.values, key = []string{x.path}, []any{x.v}, []any{k}
			usable, given = ok, x.v != nil
		} else {
			obj, _ := x.v.(map[string]any)
			for _, f := range decl.By {
				v := obj[f.Field]
				k, ok := keyOf(v, f.As)
				usable, given = usable && ok, given || v != nil
				o.paths = append(o.paths, fieldPath(x.path, f.Field))
				o.values, key = append(o.values, v), append(key, k)
			}
		}
		if !usable || !given {
			continue
		}
		data, _ := json.Marshal(key)
		keys, occs = append(keys, string(data)), append(occs, o)
	}
	return keys, occs
}

// checkUnique refuses docs, a set's documents in its order, when a value that a uniqueness
// declaration of their kinds' schemas covers repeats: within one list, or among all documents of
// the kind. It reports every repeated value at once, one line each, sorted: the documents and
// paths that hold it, the value as its first occurrence writes it, and the declaration's hint.
func checkUnique(docs Documents, s *schema.Schemas) error {
	type group struct {
		decl uniqueDecl
		occs []occurrence
	}
	groups := map[string]*group{}
	var lines []string
	for _, d := range docs {
		walkDocument(d, s, func(n node) bool {
			raw, ok := n.sch["x-chalkos-unique"]
			if !ok {
				return true
			}
			decls, err := decodeUnique(raw)
			if err != nil {
				lines = append(lines, fmt.Sprintf("%s: %s: x-chalkos-unique of the image's schema: %v", d.ID(), n.path, err))
				return true
			}
			for i, decl := range decls {
				// A declaration compares the values of one schema node, the same in every document
				// of the kind, and across "list" those of one list alone.
				scope := d.APIVersion + " " + d.Kind + " " + n.field + " " + strconv.Itoa(i)
				if decl.Across == "list" {
					scope += " " + d.Name + " " + n.path
				}
				keys, occs := occurrences(d.ID(), n, decl)
				for j, k := range keys {
					id := scope + "\x00" + k
					g := groups[id]
					if g == nil {
						g = &group{decl: decl}
						groups[id] = g
					}
					g.occs = append(g.occs, occs[j])
				}
			}
			return true
		})
	}
	for _, g := range groups {
		if len(g.occs) > 1 {
			lines = append(lines, uniqueLine(g.occs, g.decl.Hint))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	slices.Sort(lines)
	return errors.New(strings.Join(lines, "\n"))
}

// uniqueLine is the refusal of a repeated key. Where every occurrence has one path, each in a
// document of its own, it names the documents and then the path; otherwise each document,
// followed by its paths, and a colon before the value.
func uniqueLine(occs []occurrence, hint string) string {
	sameLabel := true
	for _, o := range occs[1:] {
		sameLabel = sameLabel && o.label() == occs[0].label()
	}
	var parts []string
	for i, o := range occs {
		switch {
		case sameLabel:
			parts = append(parts, o.doc.String())
		case i == 0 || o.doc != occs[i-1].doc:
			parts = append(parts, o.doc.String()+": "+o.label())
		default:
			parts = append(parts, o.label())
		}
	}
	where := strings.Join(parts, ", ") + ":"
	if sameLabel {
		where += " " + occs[0].label()
	}
	line := where + " " + occs[0].shown() + " is not unique"
	if hint != "" {
		line += "; " + hint
	}
	return line
}
