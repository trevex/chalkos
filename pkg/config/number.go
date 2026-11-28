package config

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/schema"
)

// maxExponent bounds the exponent of a number Integral rewrites, so 1e999999 does not become a
// million digits; such a number stays as written, which no integer field takes anyway.
const maxExponent = 1000

// Integral is a copy of the decoded JSON value v with each json.Number of an integer value in
// plain digits, such as 100.0 and 1e2 as 100. JSON Schema's integer type accepts those spellings,
// while encoding/json refuses them for an integer field and CEL reads them as doubles. Every
// document passes through it in FromObject; a schema's defaults keep the schema's spelling, so
// a value filled in from one, such as 1.0, compares with a config's value once through Integral.
func Integral(v any) any {
	return schema.MapJSON(v, func(x any) any {
		if n, ok := x.(json.Number); ok {
			return integerSpelling(n)
		}
		return x
	})
}

// integerSpelling is n in plain digits when n is an integer, else n as it is.
func integerSpelling(n json.Number) json.Number {
	s := string(n)
	if !strings.ContainsAny(s, ".eE") {
		return n
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
		if err != nil || exp > maxExponent || exp < -maxExponent {
			return n
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || !r.IsInt() {
		return n
	}
	return json.Number(r.Num().String())
}
