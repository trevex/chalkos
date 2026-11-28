package sdk

import (
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// FixedChange is a change of a field that every node of a cluster holds alike, as an image's
// schema marks it with x-chalkos-fixed: the document's kind and name, the field's path below the
// document, such as spec.domain, and its old and new values. The values are JSON values with the
// image's defaults filled in; nil stands for a value the document leaves out and the schema gives
// no default for.
type FixedChange struct {
	Kind, Name, Path string
	Old, New         any
}

// FixedChanges are the changes from old to next, the documents of one node at two times or of two
// nodes, of the fields that the contract's schemas mark as held alike by every node of a cluster,
// in the order of next's documents and of the fields' paths. A document of next is compared with
// old's document of the same kind and name, or, for a kind a node holds once, with old's one
// document of the kind whatever its name. A document that only one side holds is no change: the
// kinds' own rules cover documents that come and go. A list or a map compares as a whole, and a
// kind the contract does not declare marks nothing.
func FixedChanges(c Contract, old, next []config.Document) ([]FixedChange, error) {
	if c.Image == nil || c.Image.Schemas == nil {
		return nil, errors.New("the contract holds no schemas, so it marks no field that every node holds alike")
	}
	s := c.Image.Schemas
	var out []FixedChange
	for _, d := range next {
		paths := fixedPaths(s, d.APIVersion, d.Kind)
		if len(paths) == 0 {
			continue
		}
		o, ok := counterpart(s, old, d)
		if !ok {
			continue
		}
		before, err := s.Default(o.Object)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", o.Kind, o.Name, err)
		}
		after, err := s.Default(d.Object)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", d.Kind, d.Name, err)
		}
		for _, p := range paths {
			was, is := valueAt(before, p), valueAt(after, p)
			if !reflect.DeepEqual(was, is) {
				out = append(out, FixedChange{Kind: d.Kind, Name: d.Name, Path: schema.FieldPath(p), Old: was, New: is})
			}
		}
	}
	return out, nil
}

// fixedPaths are the fields of the kind that x-chalkos-fixed marks, each a property's name per
// level, in name order. The walk descends only where x-chalkos-fixed-within says a marked field
// lies below, and never into a list's items or a map's values, which the marks leave alone.
func fixedPaths(s *schema.Schemas, apiVersion, kind string) [][]string {
	var paths [][]string
	s.WalkSchema(apiVersion, kind, func(n schema.Node) bool {
		if len(n.Field) > 0 {
			if last := n.Field[len(n.Field)-1]; last == schema.ItemStep || last == schema.ValueStep {
				return false
			}
		}
		a := n.Annotations()
		if a.Fixed == schema.FixedCluster {
			if !slices.ContainsFunc(paths, func(p []string) bool { return slices.Equal(p, n.Field) }) {
				paths = append(paths, slices.Clone(n.Field))
			}
			return false
		}
		return a.FixedWithin
	})
	return paths
}

// counterpart is old's document that d is compared with: of the same kind and name, or old's one
// document of a kind that a node holds once.
func counterpart(s *schema.Schemas, old []config.Document, d config.Document) (config.Document, bool) {
	kinds := s.Kinds()
	if i := slices.IndexFunc(kinds, func(k schema.Kind) bool { return k.APIVersion == d.APIVersion && k.Kind == d.Kind }); i >= 0 && !kinds[i].Multiple {
		return config.Documents(old).Single(d.APIVersion, d.Kind)
	}
	i := slices.IndexFunc(old, func(o config.Document) bool {
		return o.APIVersion == d.APIVersion && o.Kind == d.Kind && o.Name == d.Name
	})
	if i < 0 {
		return config.Document{}, false
	}
	return old[i], true
}

// valueAt is the value at path, a property's name per level, in the decoded document v; nil where
// v holds none.
func valueAt(v any, path []string) any {
	for _, step := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[step]
	}
	return v
}
