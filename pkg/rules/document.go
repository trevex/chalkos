package rules

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

// documentFunction is the function document(kind, name) expands to. The parser cannot read its
// name from a rule's text, so a rule calls it through document alone.
const documentFunction = "@chalkos_document"

// documents gives change rules document(kind, name): the document of the kind by that name as
// config holds it, with its schema defaults and its secret values hashed, or null where config
// holds none, as for a null name or a name a reference reserves. The kind is a string literal;
// apiVersions maps each kind of the image to its apiVersion, which the macro writes into the call,
// so a kind the image lacks fails the rule's compilation.
func documents(apiVersions map[string]string) cel.EnvOption {
	return cel.Lib(documentLib{apiVersions})
}

type documentLib struct{ apiVersions map[string]string }

func (l documentLib) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Macros(cel.GlobalMacro("document", 2, l.expand)),
		cel.Function(documentFunction,
			cel.Overload("chalkos_document", []*cel.Type{cel.DynType, cel.StringType, cel.StringType, cel.DynType}, cel.DynType,
				cel.FunctionBinding(lookupDocument))),
	}
}

func (documentLib) ProgramOptions() []cel.ProgramOption { return nil }

// expand turns document(kind, name) into the lookup of name among config's documents of the
// kind's apiVersion and kind.
func (l documentLib) expand(eh cel.MacroExprFactory, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
	kind, ok := stringLiteral(args[0])
	if !ok {
		return nil, eh.NewError(args[0].ID(), `document: the kind is a string literal, such as "VRF"`)
	}
	apiVersion, ok := l.apiVersions[kind]
	if !ok {
		return nil, eh.NewError(args[0].ID(), fmt.Sprintf("document: the image declares no kind %s; it declares %s", kind, strings.Join(slices.Sorted(maps.Keys(l.apiVersions)), ", ")))
	}
	return eh.NewCall(documentFunction, eh.NewIdent("config"), eh.NewLiteral(types.String(apiVersion)), eh.NewLiteral(types.String(kind)), args[1]), nil
}

// stringLiteral is the string e is, when it is a string literal.
func stringLiteral(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().(types.String)
	return string(s), ok
}

// lookupDocument is config[apiVersion][kind][name], or null where config holds none of them or
// name is no string, as a null reference is.
func lookupDocument(args ...ref.Val) ref.Val {
	v := args[0]
	for _, key := range args[1:] {
		if _, ok := key.(types.String); !ok {
			return types.NullValue
		}
		m, ok := v.(traits.Mapper)
		if !ok {
			return types.NullValue
		}
		if v, ok = m.Find(key); !ok {
			return types.NullValue
		}
	}
	return v
}
