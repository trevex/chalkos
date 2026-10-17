// Package rules compiles the change rules an image's schemas declare, CEL expressions on kinds
// and on fields, and evaluates them against a node's config, the config it replaces and the
// node's facts. chalkd evaluates every rule; chalkctl those that read no facts.
package rules

import (
	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/env"
	"cel.dev/cel-go/common/overloads"
	"cel.dev/cel-go/ext"
)

// newEnv is the environment rules compile in: CEL's standard library without bytes(string), the
// lists, sets, strings and bindings extensions, chalkos's library, and the variables given, all
// of type dyn, since documents are JSON whose shape the schemas, not CEL, check.
func newEnv(variables ...string) (*cel.Env, error) {
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
	}
	for _, v := range variables {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}
	return cel.NewCustomEnv(opts...)
}
