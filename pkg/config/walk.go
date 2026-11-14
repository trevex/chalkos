package config

import "github.com/trevex/chalkos/pkg/schema"

// walk is schema.Walk of d from the document's root; it walks nothing without schemas.
func walk(d Document, schemas *schema.Schemas, visit func(schema.Node) bool) {
	if schemas != nil {
		schemas.Walk(d.APIVersion, d.Kind, d.Object, visit)
	}
}
