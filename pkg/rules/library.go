package rules

import (
	"fmt"
	"math"
	"math/big"
	"net/netip"
	"reflect"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

	"github.com/trevex/chalkos/pkg/storage"
)

// The types of chalkos's own functions. They are opaque: rules compare them, convert them to
// strings and call their member functions.
var (
	cidrType = types.NewOpaqueType("cidr")
	ipType   = types.NewOpaqueType("ip")
)

// library holds the functions rules use besides CEL's own and its lists, sets, strings and
// bindings extensions: address ranges and addresses, which compare by what they denote rather
// than how they are spelt, and sizes as the storage kinds write them.
func library() cel.EnvOption {
	return cel.Lib(chalkosLib{})
}

type chalkosLib struct{}

func (chalkosLib) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("cidr",
			cel.Overload("chalkos_string_to_cidr", []*cel.Type{cel.StringType}, cidrType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					p, err := netip.ParsePrefix(string(v.(types.String)))
					if err != nil {
						return types.NewErr("cidr: the argument is not an address range")
					}
					return cidr{p.Masked()}
				}))),
		cel.Function("ip",
			cel.Overload("chalkos_string_to_ip", []*cel.Type{cel.StringType}, ipType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					a, err := netip.ParseAddr(string(v.(types.String)))
					if err != nil {
						return types.NewErr("ip: the argument is not an address")
					}
					return ip{a}
				})),
			cel.MemberOverload("chalkos_cidr_ip_int", []*cel.Type{cidrType, cel.IntType}, ipType,
				cel.BinaryBinding(func(c, n ref.Val) ref.Val {
					return c.(cidr).nth(int64(n.(types.Int)))
				}))),
		cel.Function("prefix",
			cel.MemberOverload("chalkos_cidr_prefix", []*cel.Type{cidrType}, cel.IntType,
				cel.UnaryBinding(func(c ref.Val) ref.Val {
					return types.Int(c.(cidr).Bits())
				}))),
		cel.Function("contains",
			cel.MemberOverload("chalkos_cidr_contains_ip", []*cel.Type{cidrType, ipType}, cel.BoolType,
				cel.BinaryBinding(func(c, a ref.Val) ref.Val {
					return types.Bool(c.(cidr).Contains(a.(ip).Addr))
				}))),
		cel.Function("family",
			cel.MemberOverload("chalkos_ip_family", []*cel.Type{ipType}, cel.StringType,
				cel.UnaryBinding(func(a ref.Val) ref.Val {
					if a.(ip).Is4() {
						return types.String("ipv4")
					}
					return types.String("ipv6")
				}))),
		cel.Function("string",
			cel.Overload("chalkos_cidr_to_string", []*cel.Type{cidrType}, cel.StringType,
				cel.UnaryBinding(func(c ref.Val) ref.Val { return types.String(c.(cidr).String()) })),
			cel.Overload("chalkos_ip_to_string", []*cel.Type{ipType}, cel.StringType,
				cel.UnaryBinding(func(a ref.Val) ref.Val { return types.String(a.(ip).String()) }))),
		// The standard library's bytes(string), which converts to CEL's bytes type, is left out
		// of the environment, so the name reads sizes.
		cel.Function("bytes",
			cel.Overload("chalkos_size_to_bytes", []*cel.Type{cel.StringType}, cel.IntType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					n, ok := size(string(v.(types.String)))
					if !ok {
						return types.NewErr("bytes: the argument is not a size below 8 EiB, digits with an optional K, M, G, T or P suffix")
					}
					return types.Int(n)
				}))),
		cel.Function("isSize",
			cel.Overload("chalkos_string_is_size", []*cel.Type{cel.StringType}, cel.BoolType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					_, ok := size(string(v.(types.String)))
					return types.Bool(ok)
				}))),
	}
}

// size is s in bytes, if it is a size bytes() reads: one of ParseSize below 2^63.
func size(s string) (int64, bool) {
	n, err := storage.ParseSize(s)
	if err != nil || n > math.MaxInt64 {
		return 0, false
	}
	return int64(n), true
}

func (chalkosLib) ProgramOptions() []cel.ProgramOption { return nil }

// cidr is an address range, its address masked, so that spellings of one range, such as
// fd00:0::/108 and fd00::/108, or 10.244.1.0/16 and 10.244.0.0/16, are equal.
type cidr struct{ netip.Prefix }

// nth is the address n places after the range's first, which must lie in the range.
func (c cidr) nth(n int64) ref.Val {
	if n < 0 {
		return types.NewErr("<cidr>.ip: the index is negative")
	}
	b := new(big.Int).SetBytes(c.Addr().AsSlice())
	b.Add(b, big.NewInt(n))
	if b.BitLen() > c.Addr().BitLen() {
		return types.NewErr("<cidr>.ip: the index is past the range")
	}
	a, _ := netip.AddrFromSlice(b.FillBytes(make([]byte, c.Addr().BitLen()/8)))
	if !c.Contains(a) {
		return types.NewErr("<cidr>.ip: the index is past the range")
	}
	return ip{a}
}

func (c cidr) ConvertToNative(typeDesc reflect.Type) (any, error) {
	if typeDesc.Kind() == reflect.String {
		return c.String(), nil
	}
	return nil, fmt.Errorf("a cidr does not convert to %v", typeDesc)
}

func (c cidr) ConvertToType(t ref.Type) ref.Val {
	switch t {
	case types.StringType:
		return types.String(c.String())
	case cidrType:
		return c
	case types.TypeType:
		return cidrType
	}
	return types.NewErr("a cidr does not convert to %s", t)
}

func (c cidr) Equal(other ref.Val) ref.Val {
	o, ok := other.(cidr)
	return types.Bool(ok && c.Prefix == o.Prefix)
}

func (c cidr) Type() ref.Type { return cidrType }
func (c cidr) Value() any     { return c.Prefix }

// ip is an address, which compares as the address it denotes.
type ip struct{ netip.Addr }

func (a ip) ConvertToNative(typeDesc reflect.Type) (any, error) {
	if typeDesc.Kind() == reflect.String {
		return a.String(), nil
	}
	return nil, fmt.Errorf("an ip does not convert to %v", typeDesc)
}

func (a ip) ConvertToType(t ref.Type) ref.Val {
	switch t {
	case types.StringType:
		return types.String(a.String())
	case ipType:
		return a
	case types.TypeType:
		return ipType
	}
	return types.NewErr("an ip does not convert to %s", t)
}

func (a ip) Equal(other ref.Val) ref.Val {
	o, ok := other.(ip)
	return types.Bool(ok && a.Addr == o.Addr)
}

func (a ip) Type() ref.Type { return ipType }
func (a ip) Value() any     { return a.Addr }
