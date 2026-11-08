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

	"github.com/trevex/chalkos/pkg/imageref"
	"github.com/trevex/chalkos/pkg/schema"
)

// normalisers turn a string into the form it compares in, and refuse a string they cannot read,
// one for each of schema.Normalisers. ip and cidr compare as the rules' ip() and cidr() do, and host as RegistriesOf compares
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
		h, err := imageref.RegistryHost(s)
		return h, err == nil
	},
	"lower": func(s string) (string, bool) { return strings.ToLower(s), true },
}

// keyOf is the form v compares in, tagged so that no two kinds of value share an encoding:
// normalised with as, which takes strings alone, and a number as the exact rational it is, so
// that 100 and 1e2 compare equal and 0.5 and 0.25 apart. ok is false for a value the normaliser
// cannot read.
func keyOf(v any, as string) (key [2]any, ok bool) {
	if v == nil {
		return [2]any{"value", nil}, true
	}
	if as != "" {
		s, isString := v.(string)
		if !isString {
			return key, false
		}
		n, ok := normalisers[as](s)
		return [2]any{"value", n}, ok
	}
	if n, isNumber := v.(json.Number); isNumber {
		if r, ok := new(big.Rat).SetString(string(n)); ok {
			return [2]any{"number", r.RatString()}, true
		}
	}
	return [2]any{"value", v}, true
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
// no part, nor does one with a value its normaliser cannot read, which the field's type or a rule
// has to refuse; a null member of a tuple compares equal to another null.
func occurrences(doc ID, n schema.Node, decl schema.Unique) (keys []string, occs []occurrence) {
	type at struct {
		v    any
		path string
	}
	var values []at
	if items, ok := n.Value.([]any); ok {
		for i, item := range items {
			values = append(values, at{item, n.Path + "[" + strconv.Itoa(i) + "]"})
		}
	} else {
		values = append(values, at{n.Value, n.Path})
	}
	for _, x := range values {
		o := occurrence{doc: doc}
		var key [][2]any
		usable, given := true, false
		if len(decl.By) == 0 {
			k, ok := keyOf(x.v, decl.As)
			o.paths, o.values, key = []string{x.path}, []any{x.v}, [][2]any{k}
			usable, given = ok, x.v != nil
		} else {
			obj, _ := x.v.(map[string]any)
			for _, f := range decl.By {
				v := obj[f.Field]
				k, ok := keyOf(v, f.As)
				usable, given = usable && ok, given || v != nil
				o.paths = append(o.paths, schema.PropertyPath(x.path, f.Field))
				o.values, key = append(o.values, v), append(key, k)
			}
		}
		if !usable || !given {
			continue
		}
		// The values are as Parse and FromJSON decode them, which JSON always encodes.
		data, err := json.Marshal(key)
		if err != nil {
			panic(fmt.Sprintf("%s: %s: encoding a unique key: %v", doc, x.path, err))
		}
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
		decl schema.Unique
		occs []occurrence
	}
	// A group is a key among the values the i-th declaration of a schema node compares: the node's
	// values in every document of the kind, or across "list" those of one list alone, which name
	// and path pick out.
	type groupID struct {
		apiVersion, kind, field string
		i                       int
		name, path, key         string
	}
	groups := map[groupID]*group{}
	var lines []string
	for _, d := range docs {
		walk(d, s, func(n schema.Node) bool {
			for i, decl := range n.Annotations().Unique {
				scope := groupID{apiVersion: d.APIVersion, kind: d.Kind, field: schema.FieldPath(n.Field), i: i}
				if decl.Across == "list" {
					scope.name, scope.path = d.Name, n.Path
				}
				keys, occs := occurrences(d.ID(), n, decl)
				for j, k := range keys {
					id := scope
					id.key = k
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
