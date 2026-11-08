package config

import "github.com/trevex/chalkos/pkg/schema"

// walk walks d alongside its kind's schema in schemas, as schema.Walk does, from the document's
// root; it walks nothing without schemas.
func walk(d Document, schemas *schema.Schemas, visit func(schema.Node) bool) {
	if schemas != nil {
		schemas.Walk(d.APIVersion, d.Kind, d.Object, visit)
	}
}
