package schema

import (
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// maxExpandedSize bounds a kind's schema expanded into a tree: every schema node reachable from
// its root, counted once for every path that reaches it through $ref, allOf, anyOf, oneOf,
// properties, items and the other keywords that hold schemas. Validating a document and filling
// in its defaults follow those paths, so their work grows with this size, which a schema that
// refers to one definition from two places at every level makes exponential in the schema's own
// length. A schema its reader chose, as an upgrade's header carries, could otherwise keep chalkd
// busy for hours or exhaust its memory from a few kilobytes.
//
// chalkos's schemas are far below it, as measured on the test images of chalkos 0.1.0: networkd's
// Network expands to 5414 nodes, its NetDev to 2158 and its Link to 950, and the built-in kinds to
// at most 50 (Kubernetes).
const maxExpandedSize = 100_000

var (
	errTooLarge = fmt.Errorf("the schema expands to more than %d schemas through its references", maxExpandedSize)
	errCycle    = errors.New("the schema refers to itself, which no chalkos kind does")
	errDynamic  = errors.New("the schema uses $dynamicRef or $recursiveRef, which no chalkos kind does")
)

// checkSize refuses a schema whose expanded size exceeds maxExpandedSize, one that refers to
// itself, through which that size would be unbounded, and one with a dynamic reference, which
// the size cannot follow.
func checkSize(root *jsonschema.Schema) error {
	_, err := expandedSize(root, map[*jsonschema.Schema]int{}, map[*jsonschema.Schema]bool{})
	return err
}

// expandedSize returns the expanded size of sch, at most maxExpandedSize+1, memoised in sizes;
// onPath holds the schemas between the root and sch, which a cycle returns to.
func expandedSize(sch *jsonschema.Schema, sizes map[*jsonschema.Schema]int, onPath map[*jsonschema.Schema]bool) (int, error) {
	if n, ok := sizes[sch]; ok {
		return n, nil
	}
	if onPath[sch] {
		return 0, errCycle
	}
	if sch.RecursiveRef != nil || sch.DynamicRef != nil {
		return 0, errDynamic
	}
	onPath[sch] = true
	defer delete(onPath, sch)
	total := 1
	for _, child := range children(sch) {
		n, err := expandedSize(child, sizes, onPath)
		if err != nil {
			return 0, err
		}
		total += n
		if total > maxExpandedSize {
			return 0, errTooLarge
		}
	}
	sizes[sch] = total
	return total, nil
}

// children returns the schemas sch holds or refers to, in every keyword that holds one.
func children(sch *jsonschema.Schema) []*jsonschema.Schema {
	var out []*jsonschema.Schema
	add := func(s ...*jsonschema.Schema) {
		for _, c := range s {
			if c != nil {
				out = append(out, c)
			}
		}
	}
	addAny := func(v any) {
		switch v := v.(type) {
		case *jsonschema.Schema:
			add(v)
		case []*jsonschema.Schema:
			add(v...)
		}
	}
	add(sch.Ref, sch.Not, sch.If, sch.Then, sch.Else, sch.PropertyNames, sch.UnevaluatedProperties,
		sch.Contains, sch.Items2020, sch.UnevaluatedItems, sch.ContentSchema)
	add(sch.AllOf...)
	add(sch.AnyOf...)
	add(sch.OneOf...)
	add(sch.PrefixItems...)
	for _, c := range sch.Properties {
		add(c)
	}
	for _, c := range sch.PatternProperties {
		add(c)
	}
	for _, c := range sch.DependentSchemas {
		add(c)
	}
	for _, c := range sch.Dependencies {
		addAny(c)
	}
	addAny(sch.AdditionalProperties)
	addAny(sch.Items)
	addAny(sch.AdditionalItems)
	return out
}
