package config

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Leaf is a value of a document that holds no other: a scalar, or an empty object or list, as
// JSON without HTML escaping, at its path. A list's elements have their index as their path
// element.
type Leaf struct {
	Path  []string
	Value string
}

// Leaves returns the leaves of v keyed by their path, its elements joined by NUL.
func Leaves(v any) map[string]Leaf {
	out := map[string]Leaf{}
	addLeaves(nil, v, out)
	return out
}

func addLeaves(path []string, v any, out map[string]Leaf) {
	add := func(k string, child any) {
		addLeaves(append(slices.Clone(path), k), child, out)
	}
	switch v := v.(type) {
	case map[string]any:
		if len(v) > 0 {
			for k, child := range v {
				add(k, child)
			}
			return
		}
	case []any:
		if len(v) > 0 {
			for i, child := range v {
				add(strconv.Itoa(i), child)
			}
			return
		}
	}
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	out[strings.Join(path, "\x00")] = Leaf{Path: path, Value: strings.TrimSuffix(b.String(), "\n")}
}

// ComparePaths orders paths element by element, list indexes by their number, a path before the
// paths below it.
func ComparePaths(a, b []string) int {
	for i := range min(len(a), len(b)) {
		x, errX := strconv.Atoi(a[i])
		y, errY := strconv.Atoi(b[i])
		if c := cmp.Compare(a[i], b[i]); errX == nil && errY == nil {
			c = cmp.Compare(x, y)
			if c != 0 {
				return c
			}
		} else if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

// ShowPath writes a path below v as the field it names: keys joined by dots, list indexes in
// brackets.
func ShowPath(v any, path []string) string {
	var b strings.Builder
	for i, p := range path {
		if list, ok := v.([]any); ok {
			fmt.Fprintf(&b, "[%s]", p)
			if n, err := strconv.Atoi(p); err == nil && n < len(list) {
				v = list[n]
			} else {
				v = nil
			}
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(p)
		m, _ := v.(map[string]any)
		v = m[p]
	}
	return b.String()
}
