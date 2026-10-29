package chalkctl

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
)

// docChange is a document that differs between a node's config slot and the inventory: added
// ('+'), removed ('-'), or changed ('~') in the fields listed.
type docChange struct {
	op     byte
	doc    config.Document
	fields []fieldChange
}

// fieldChange is a field that differs, by its path and as it is shown, with its old and new value
// as JSON, empty where the field is missing; secret fields show neither.
type fieldChange struct {
	path           []string
	name, old, new string
	secret         bool
}

// configDiff is what delivering the inventory's config would change in a slot, ordered by
// apiVersion, kind and name.
type configDiff []docChange

// diffConfig compares node, a slot's documents as GetConfig returns them, with inventory, the
// inventory's for the node, both as delivered: no defaults are filled in, so a field one spells out
// and the other leaves to its default differs. The inventory's documents are redacted as the
// node's are, so secret values compare by their hashes and show as changed alone.
func diffConfig(node, inventory []config.Document) configDiff {
	key := func(d config.Document) string { return d.APIVersion + " " + d.Kind + " " + d.Name }
	was := map[string]config.Document{}
	for _, d := range node {
		was[key(d)] = d
	}
	var changes configDiff
	for _, d := range config.Redact(inventory) {
		old, ok := was[key(d)]
		delete(was, key(d))
		if !ok {
			changes = append(changes, docChange{op: '+', doc: d})
			continue
		}
		if fields := diffFields(old, d); len(fields) > 0 {
			changes = append(changes, docChange{op: '~', doc: d, fields: fields})
		}
	}
	for _, d := range was {
		changes = append(changes, docChange{op: '-', doc: d})
	}
	slices.SortFunc(changes, func(a, b docChange) int {
		return cmp.Or(cmp.Compare(a.doc.APIVersion, b.doc.APIVersion), cmp.Compare(a.doc.Kind, b.doc.Kind), cmp.Compare(a.doc.Name, b.doc.Name))
	})
	return changes
}

// diffFields lists the fields whose values differ between two versions of a document, by path.
func diffFields(old, new config.Document) []fieldChange {
	a, b := map[string]leaf{}, map[string]leaf{}
	flatten(nil, old.Object, a)
	flatten(nil, new.Object, b)
	var fields []fieldChange
	for k, la := range a {
		lb, ok := b[k]
		if ok && la.value == lb.value {
			continue
		}
		f := fieldChange{path: la.path, name: showPath(old, la.path), old: la.value, new: lb.value}
		f.secret = config.Redacted(old, f.path) || config.Redacted(new, f.path)
		fields = append(fields, f)
	}
	for k, lb := range b {
		if _, ok := a[k]; !ok {
			fields = append(fields, fieldChange{path: lb.path, name: showPath(new, lb.path), new: lb.value, secret: config.Redacted(new, lb.path)})
		}
	}
	slices.SortFunc(fields, func(x, y fieldChange) int { return comparePaths(x.path, y.path) })
	return fields
}

// leaf is a value of a document that holds no other: a scalar, or an empty object or list, as
// JSON, at its path.
type leaf struct {
	path  []string
	value string
}

// flatten adds the leaves of v below path to out, keyed by their path. A list's elements have
// their index as their path element.
func flatten(path []string, v any, out map[string]leaf) {
	add := func(k string, child any) {
		flatten(append(slices.Clone(path), k), child, out)
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
	b, _ := json.Marshal(v)
	out[strings.Join(path, "\x00")] = leaf{path: path, value: string(b)}
}

// comparePaths orders paths element by element, list indexes by their number, a path before the
// paths below it.
func comparePaths(a, b []string) int {
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

// showPath writes a path as the field it names: keys joined by dots, list indexes in brackets.
func showPath(doc config.Document, path []string) string {
	var b strings.Builder
	var v any = doc.Object
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

// summary is the line a delivery prints before it changes a node: how many documents it adds,
// removes and changes, the removed ones by name.
func (c configDiff) summary() string {
	if len(c) == 0 {
		return "config unchanged"
	}
	var added, changed int
	var removed []string
	for _, d := range c {
		switch d.op {
		case '+':
			added++
		case '-':
			removed = append(removed, d.doc.String())
		default:
			changed++
		}
	}
	line := fmt.Sprintf("config %d added, %d removed", added, len(removed))
	if len(removed) > 0 {
		line += " (" + strings.Join(removed, ", ") + ")"
	}
	return fmt.Sprintf("%s, %d changed", line, changed)
}

// write writes the whole diff, a line per document and one per changed field, the path with the
// old and the new value, or that a secret field changed.
func (c configDiff) write(w io.Writer) {
	for _, d := range c {
		switch d.op {
		case '+':
			fmt.Fprintf(w, "+ %s\n", d.doc)
		case '-':
			fmt.Fprintf(w, "- %s (on the node, not in the inventory)\n", d.doc)
		default:
			fmt.Fprintf(w, "~ %s\n", d.doc)
			for _, f := range d.fields {
				if f.secret {
					fmt.Fprintf(w, "    %s: changed\n", f.name)
					continue
				}
				fmt.Fprintf(w, "    %s: %s → %s\n", f.name, cmp.Or(f.old, "(none)"), cmp.Or(f.new, "(none)"))
			}
		}
	}
}

// slotConfig asks a node for the documents of a slot, the running image's or with staged the
// staged image's; none when the node holds none for the image.
func slotConfig(ctx context.Context, conn *client.Conn, staged bool) ([]config.Document, error) {
	return getConfig(ctx, conn, &nodev1.GetConfigRequest{Staged: staged})
}

// previousConfig asks a node for the documents of the running image's previous config; none when
// the node keeps none.
func previousConfig(ctx context.Context, conn *client.Conn) ([]config.Document, error) {
	return getConfig(ctx, conn, &nodev1.GetConfigRequest{Previous: true})
}

// getConfig asks a node for the documents the request names; none when the node holds none.
func getConfig(ctx context.Context, conn *client.Conn, req *nodev1.GetConfigRequest) ([]config.Document, error) {
	resp, err := conn.GetConfig(ctx, connect.NewRequest(req))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ask for the config it holds: %w", err)
	}
	return config.Parse(strings.NewReader(resp.Msg.Documents))
}
