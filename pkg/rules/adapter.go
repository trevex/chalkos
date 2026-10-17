package rules

import (
	"fmt"
	"reflect"
	"slices"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

// adapter converts the JSON rules read, the documents and the facts, into CEL values whose maps
// iterate in key order. CEL's own maps iterate in Go's random order, so a message listing the
// documents a rule refuses would name them in another order each time; sort() would order them
// too, but its cost cannot be estimated for a list of dyn.
type adapter struct{}

func (adapter) NativeToValue(v any) ref.Val {
	switch v := v.(type) {
	case map[string]any:
		return sortedMap{types.NewStringInterfaceMap(adapter{}, v)}
	case []any:
		return types.NewDynamicList(adapter{}, v)
	}
	return types.DefaultTypeAdapter.NativeToValue(v)
}

// sortedMap is a map whose keys, strings, iterate in order. It is not Foldable, so comprehensions
// over two variables iterate it through Iterator too.
type sortedMap struct{ traits.Mapper }

func (m sortedMap) Iterator() traits.Iterator {
	var keys []string
	for it := m.Mapper.Iterator(); it.HasNext() == types.True; {
		keys = append(keys, string(it.Next().(types.String)))
	}
	slices.Sort(keys)
	return &keyIterator{keys: keys}
}

type keyIterator struct {
	keys []string
	next int
}

func (it *keyIterator) HasNext() ref.Val { return types.Bool(it.next < len(it.keys)) }

func (it *keyIterator) Next() ref.Val {
	if it.next >= len(it.keys) {
		return nil
	}
	it.next++
	return types.String(it.keys[it.next-1])
}

func (it *keyIterator) ConvertToNative(t reflect.Type) (any, error) {
	return nil, fmt.Errorf("an iterator does not convert to %v", t)
}

func (it *keyIterator) ConvertToType(ref.Type) ref.Val {
	return types.NewErr("an iterator does not convert")
}

func (it *keyIterator) Equal(ref.Val) ref.Val { return types.False }
func (it *keyIterator) Type() ref.Type        { return types.IteratorType }
func (it *keyIterator) Value() any            { return nil }
