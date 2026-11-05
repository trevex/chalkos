package rules

import (
	"fmt"
	"math"
	"math/big"
	"net/netip"
	"reflect"
	"strconv"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/checker"
	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/interpreter"

	"github.com/trevex/chalkos/pkg/dns"
	"github.com/trevex/chalkos/pkg/schema"
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
// than how they are spelt, sizes as the storage kinds write them, ranges of ports as the router
// kinds write them, and DNS names and record data as the Resolver's local zones hold them.
func library() cel.EnvOption {
	return cel.Lib(celLib{})
}

type celLib struct{}

// The errors of chalkos's functions. They hold no value, so a refusal shows them whole.
const (
	errNotCIDR       = "cidr: the argument is not an address range"
	errNotIP         = "ip: the argument is not an address"
	errNotSize       = "bytes: the argument is not a size below 8 EiB, digits with an optional K, M, G, T or P suffix"
	errNegativeIndex = "<cidr>.ip: the index is negative"
	errPastRange     = "<cidr>.ip: the index is past the range"
	errNotPortRange  = "portRange: the argument is not a port or a range of ports from 1 to 65535, the first no higher than the last"
)

// portRangeType is what portRange() yields: a map of first and last, both ints.
var portRangeType = cel.MapType(cel.StringType, cel.IntType)

// The overloads whose cost grows with the length of a string they read, by the index of that
// argument: they scan it once, at the cost per character CEL's own string functions take. Every
// other function of the library costs one step.
var stringScans = map[string]int{
	"chalkos_string_is_dns_name":           0,
	"chalkos_string_string_is_record_data": 1,
}

func (celLib) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("cidr",
			cel.Overload("chalkos_string_to_cidr", []*cel.Type{cel.StringType}, cidrType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					p, err := netip.ParsePrefix(string(v.(types.String)))
					if err != nil {
						return types.NewErr("%s", errNotCIDR)
					}
					return cidr{p.Masked()}
				}))),
		cel.Function("ip",
			cel.Overload("chalkos_string_to_ip", []*cel.Type{cel.StringType}, ipType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					a, err := netip.ParseAddr(string(v.(types.String)))
					if err != nil {
						return types.NewErr("%s", errNotIP)
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
		cel.Function("last",
			cel.MemberOverload("chalkos_cidr_last", []*cel.Type{cidrType}, ipType,
				cel.UnaryBinding(func(c ref.Val) ref.Val {
					return c.(cidr).last()
				}))),
		cel.Function("compare",
			cel.MemberOverload("chalkos_ip_compare_ip", []*cel.Type{ipType, ipType}, cel.IntType,
				cel.BinaryBinding(func(a, b ref.Val) ref.Val {
					return types.Int(a.(ip).Compare(b.(ip).Addr))
				}))),
		cel.Function("portRange",
			cel.Overload("chalkos_int_to_port_range", []*cel.Type{cel.IntType}, portRangeType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					return portRangeOf(strconv.FormatInt(int64(v.(types.Int)), 10))
				})),
			cel.Overload("chalkos_string_to_port_range", []*cel.Type{cel.StringType}, portRangeType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					return portRangeOf(string(v.(types.String)))
				}))),
		cel.Function("isDNSName",
			cel.Overload("chalkos_string_is_dns_name", []*cel.Type{cel.StringType}, cel.BoolType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					return types.Bool(dns.IsName(string(v.(types.String))))
				}))),
		cel.Function("isRecordData",
			cel.Overload("chalkos_string_string_is_record_data", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
				cel.BinaryBinding(func(t, v ref.Val) ref.Val {
					return types.Bool(dns.IsRecordData(string(t.(types.String)), string(v.(types.String))))
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
						return types.NewErr("%s", errNotSize)
					}
					return types.Int(n)
				}))),
		cel.Function("isSize",
			cel.Overload("chalkos_string_is_size", []*cel.Type{cel.StringType}, cel.BoolType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					_, ok := size(string(v.(types.String)))
					return types.Bool(ok)
				}))),
		stringScanCosts(),
	}
}

// stringScanCosts estimates the overloads of stringScans at one step and a tenth of a step per
// character of the string they scan, as CEL estimates contains() and the other string functions
// that read a string whole.
func stringScanCosts() cel.EnvOption {
	var opts []checker.CostOption
	for id, arg := range stringScans {
		opts = append(opts, checker.OverloadCostEstimate(id, func(e checker.CostEstimator, _ *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
			sz := args[arg].ComputedSize()
			if sz == nil {
				sz = e.EstimateSize(args[arg])
			}
			if sz == nil {
				sz = &checker.SizeEstimate{Min: 0, Max: math.MaxUint64}
			}
			scan := sz.MultiplyByCostFactor(common.StringTraversalCostFactor)
			return &checker.CallEstimate{CostEstimate: scan.Add(checker.FixedCostEstimate(1))}
		}))
	}
	return cel.CostEstimatorOptions(opts...)
}

// portRangeOf is the first and last port of s, as PortRange reads it, as portRange() yields
// them.
func portRangeOf(s string) ref.Val {
	first, last, ok := schema.PortRange(s)
	if !ok {
		return types.NewErr("%s", errNotPortRange)
	}
	return types.NewStringInterfaceMap(types.DefaultTypeAdapter, map[string]any{"first": first, "last": last})
}

// size is s in bytes, if it is a size bytes() reads: one of ParseSize below 2^63.
func size(s string) (int64, bool) {
	n, err := storage.ParseSize(s)
	if err != nil || n > math.MaxInt64 {
		return 0, false
	}
	return int64(n), true
}

// ProgramOptions count the overloads of stringScans at runtime as their estimate does, on the
// string they meet.
func (celLib) ProgramOptions() []cel.ProgramOption {
	var opts []interpreter.CostTrackerOption
	for id, arg := range stringScans {
		opts = append(opts, interpreter.OverloadCostTracker(id, func(args []ref.Val, _ ref.Val) *uint64 {
			n := uint64(1)
			if s, ok := args[arg].(types.String); ok {
				n += uint64(math.Ceil(float64(len(s)) * common.StringTraversalCostFactor))
			}
			return &n
		}))
	}
	return []cel.ProgramOption{cel.CostTrackerOptions(opts...)}
}

// cidr is an address range, its address masked, so that spellings of one range, such as
// fd00:0::/108 and fd00::/108, or 10.244.1.0/16 and 10.244.0.0/16, are equal.
type cidr struct{ netip.Prefix }

// nth is the address n places after the range's first, which must lie in the range.
func (c cidr) nth(n int64) ref.Val {
	if n < 0 {
		return types.NewErr("%s", errNegativeIndex)
	}
	b := new(big.Int).SetBytes(c.Addr().AsSlice())
	b.Add(b, big.NewInt(n))
	if b.BitLen() > c.Addr().BitLen() {
		return types.NewErr("%s", errPastRange)
	}
	a, _ := netip.AddrFromSlice(b.FillBytes(make([]byte, c.Addr().BitLen()/8)))
	if !c.Contains(a) {
		return types.NewErr("%s", errPastRange)
	}
	return ip{a}
}

// last is the range's last address: all of its host bits set, IPv4's broadcast address.
func (c cidr) last() ref.Val {
	b := c.Addr().AsSlice()
	for i := c.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
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
