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
// image's defaults filled in and integers in plain digits; nil stands for no value: left out
// without a default, written as null, or below a group that is null or left out.
type FixedChange struct {
	Kind, Name, Path string
	Old, New         any
}

// FixedChanges are the changes from old to next, the documents of one node at two times or of two
// nodes, of the fields that the contract's schemas mark as held alike by every node of a cluster,
// in the order of next's documents and, per document, depth first with the fields of each level in
// name order. A document of next is compared with old's document of the same kind and name, or,
// for a kind a node holds once, with old's one document of the kind whatever its name. A document
// that only one side holds is no change: the kinds' own rules cover documents that come and go. A
// list or a map compares as a whole, and a kind the contract does not declare marks nothing. The
// kinds' generator puts no mark below a union of types other than a nullable one, so a marked
// field holds its value in whichever branch the document takes.
//
// The documents must come from config.Parse, whose numbers are json.Numbers, and be valid against
// the contract's schemas, since FixedChanges does not validate them: it compares their values with
// the defaults filled in, so a field written as null differs from one left out where the schema
// gives it a default, an integer compares by its value, one that a default spells 1.0 too, and any
// other number by its spelling, 0.5 differing from 5e-1.
func FixedChanges(c Contract, old, next []config.Document) ([]FixedChange, error) {
	if c.Image == nil || c.Image.Schemas == nil {
		return nil, errors.New("the contract holds no schemas, so it marks no field that every node holds alike")
	}
	s := c.Image.Schemas
	marks := map[schema.Kind][][]string{}
	var out []FixedChange
	for _, d := range next {
		k, ok := s.Kind(d.APIVersion, d.Kind)
		if !ok {
			continue
		}
		paths, ok := marks[k]
		if !ok {
			paths = fixedPaths(s, k.APIVersion, k.Kind)
			marks[k] = paths
		}
		if len(paths) == 0 {
			continue
		}
		o, ok := counterpart(k, old, d)
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
			was, is := config.Integral(valueAt(before, p)), config.Integral(valueAt(after, p))
			if !reflect.DeepEqual(was, is) {
				out = append(out, FixedChange{Kind: d.Kind, Name: d.Name, Path: schema.FieldPath(p), Old: was, New: is})
			}
		}
	}
	return out, nil
}

// fixedPaths are the fields of the kind that x-chalkos-fixed marks, each a property's name per
// level, depth first in name order. The walk covers the whole schema, every branch of an anyOf
// too, since a nullable field carries its mark on its anyOf and a field inside a nullable group
// on its own node below one; it stops at a marked field and never descends into a list's items or
// a map's values, which compare as a whole. The branches of an anyOf share their Field, so a path
// is kept once.
func fixedPaths(s *schema.Schemas, apiVersion, kind string) [][]string {
	var paths [][]string
	s.WalkSchema(apiVersion, kind, func(n schema.Node) bool {
		if len(n.Field) > 0 {
			if last := n.Field[len(n.Field)-1]; last == schema.ItemStep || last == schema.ValueStep {
				return false
			}
		}
		if n.Annotations().Fixed != schema.FixedCluster {
			return true
		}
		if !slices.ContainsFunc(paths, func(p []string) bool { return slices.Equal(p, n.Field) }) {
			paths = append(paths, slices.Clone(n.Field))
		}
		return false
	})
	return paths
}

// counterpart is old's document that d, a document of the kind k, is compared with: of the same
// kind and name, or old's one document of a kind that a node holds once.
func counterpart(k schema.Kind, old []config.Document, d config.Document) (config.Document, bool) {
	if !k.Multiple {
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
