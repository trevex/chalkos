package chalkctl

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
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
// inventory's for the node or another slot's, both as delivered: no defaults are filled in, so a
// field one spells out and the other leaves to its default differs. Both are redacted, so secret
// values compare by their hashes and show as changed alone.
func diffConfig(node, inventory config.Redacted) configDiff {
	was := map[config.ID]config.Document{}
	for _, d := range node.Documents() {
		was[d.ID()] = d
	}
	var changes configDiff
	for _, d := range inventory.Documents() {
		old, ok := was[d.ID()]
		delete(was, d.ID())
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
	a, b := config.Leaves(old.Object), config.Leaves(new.Object)
	var fields []fieldChange
	for k, la := range a {
		lb, ok := b[k]
		if ok && la.Value == lb.Value {
			continue
		}
		f := fieldChange{path: la.Path, name: config.ShowPath(old.Object, la.Path), old: la.Value, new: lb.Value}
		f.secret = config.SecretField(old, f.path) || config.SecretField(new, f.path)
		fields = append(fields, f)
	}
	for k, lb := range b {
		if _, ok := a[k]; !ok {
			fields = append(fields, fieldChange{path: lb.Path, name: config.ShowPath(new.Object, lb.Path), new: lb.Value, secret: config.SecretField(new, lb.Path)})
		}
	}
	slices.SortFunc(fields, func(x, y fieldChange) int { return config.ComparePaths(x.path, y.path) })
	return fields
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

// write writes the whole diff of the inventory's config against the node's, as writeRemoved does.
func (c configDiff) write(w io.Writer) {
	c.writeRemoved(w, "on the node, not in the inventory")
}

// writeRemoved writes the whole diff, a line per document and one per changed field, the path with
// the old and the new value, or that a secret field changed. A removed document's line says where
// it is and is not, as removed gives it.
func (c configDiff) writeRemoved(w io.Writer, removed string) {
	for _, d := range c {
		switch d.op {
		case '+':
			fmt.Fprintf(w, "+ %s\n", d.doc)
		case '-':
			fmt.Fprintf(w, "- %s (%s)\n", d.doc, removed)
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
func slotConfig(ctx context.Context, conn *client.Conn, staged bool) (config.Redacted, error) {
	return getConfig(ctx, conn, &nodev1.GetConfigRequest{Staged: staged})
}

// getConfig asks a node for the documents the request names, which it serves redacted; none when
// the node holds none.
func getConfig(ctx context.Context, conn *client.Conn, req *nodev1.GetConfigRequest) (config.Redacted, error) {
	resp, err := conn.GetConfig(ctx, connect.NewRequest(req))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return config.Redacted{}, nil
	}
	if err != nil {
		return config.Redacted{}, fmt.Errorf("ask for the config it holds: %w", err)
	}
	return config.ParseRedacted(strings.NewReader(resp.Msg.Documents))
}
