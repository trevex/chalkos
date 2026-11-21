// Package rules compiles the change rules an image's schemas declare, CEL expressions on kinds
// and on fields, and evaluates them against a node's config, the config it replaces and the
// node's facts. chalkd evaluates every rule; chalkctl those that read no facts.
package rules

import (
	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/env"
	"cel.dev/cel-go/common/overloads"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/ext"
)

// newEnv is the environment rules compile in: CEL's standard library without bytes(string), the
// lists, sets, strings and bindings extensions, chalkos's library, ordering across int, uint and
// double, node of nodeType, and the variables given, all of type dyn. A rule's environment adds
// self, oldSelf or documents with kindTypes.
func newEnv(variables ...string) (*cel.Env, error) {
	provider, err := newNodeProvider()
	if err != nil {
		return nil, err
	}
	std := env.NewLibrarySubset().AddExcludedFunctions(
		env.NewFunction("bytes", &env.Overload{ID: overloads.StringToBytes}))
	opts := []cel.EnvOption{
		cel.StdLib(cel.StdLibSubset(std)),
		ext.Lists(),
		ext.Sets(),
		ext.Strings(),
		ext.Bindings(),
		library(),
		cel.CustomTypeAdapter(adapter{}),
		cel.CustomTypeProvider(provider),
		cel.CrossTypeNumericComparisons(true),
		// cel-go 0.33 made cost model 1 the default, which estimates nested bindings and
		// comprehensions over config far higher; model 0 keeps the estimates the rules were
		// written against.
		cel.CostModelVersion(0),
		cel.Variable("node", nodeType),
	}
	for _, v := range variables {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}
	return cel.NewCustomEnv(opts...)
}

// nodeType is the type of node, as nodeOf gives it: a check refuses a field other than its name
// and its static addresses.
var nodeType = types.NewObjectType("chalkos.Node")

var nodeFields = map[string]*types.Type{
	"name":      types.StringType,
	"addresses": types.NewListType(types.StringType),
}

// nodeProvider declares nodeType beside the types of its Provider. As with kindTypes, the value
// stays a map, which evaluation selects from by key.
type nodeProvider struct{ types.Provider }

func newNodeProvider() (nodeProvider, error) {
	reg, err := types.NewRegistry()
	return nodeProvider{reg}, err
}

func (p nodeProvider) FindStructType(name string) (*types.Type, bool) {
	if name == nodeType.TypeName() {
		return types.NewTypeTypeWithParam(nodeType), true
	}
	return p.Provider.FindStructType(name)
}

func (p nodeProvider) FindStructFieldNames(name string) ([]string, bool) {
	if name == nodeType.TypeName() {
		return []string{"addresses", "name"}, true
	}
	return p.Provider.FindStructFieldNames(name)
}

func (p nodeProvider) FindStructFieldType(name, field string) (*types.FieldType, bool) {
	if name == nodeType.TypeName() {
		t, ok := nodeFields[field]
		if !ok {
			return nil, false
		}
		return &types.FieldType{Type: t}, true
	}
	return p.Provider.FindStructFieldType(name, field)
}
