package config

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// maxExponent bounds the exponent of a number integral rewrites, so 1e999999 does not become a
// million digits; such a number stays as written, which no integer field takes anyway.
const maxExponent = 1000

// integral rewrites, in place, each json.Number in v of an integer value into plain digits, such
// as 100.0 and 1e2 into 100, and returns v. JSON Schema's integer type accepts those spellings,
// while encoding/json refuses them for an integer field and CEL reads them as doubles.
func integral(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			v[k] = integral(x)
		}
	case []any:
		for i, x := range v {
			v[i] = integral(x)
		}
	case json.Number:
		return integerSpelling(v)
	}
	return v
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
