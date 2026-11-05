package schema

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// formats are the values of "format" that the kinds' schemas assert, besides JSON Schema's own:
// an address, an address range and a range of ports, each read as the rules' ip(), cidr() and
// portRange() and the renderers read them, so that a value the schema accepts parses everywhere.
// A JSON Schema tool that knows none of them takes them as annotations.
var formats = []*jsonschema.Format{
	{Name: "ip", Validate: validateIP},
	{Name: "cidr", Validate: validateCIDR},
	{Name: "port-range", Validate: validatePortRange},
}

// The errors of the formats, which follow "'<value>' is not valid <format>: ".
var (
	errNotIP        = errors.New("an address is an IPv4 address of four numbers from 0 to 255 without leading zeros, or an IPv6 address without a zone")
	errNotCIDR      = errors.New("an address range is an address, a slash and a prefix length of at most 32 for IPv4 or 128 for IPv6")
	errNotPortRange = errors.New(`a range of ports is "<first>-<last>", each from 1 to 65535, and the first no higher than the last`)
	errQuotedPort   = errors.New(errNotPortRange.Error() + "; a single port is a number, unquoted")
)

// validateIP accepts the addresses netip.ParseAddr reads, without a zone. It accepts any value
// that is no string, which "type" checks.
func validateIP(v any) error {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if a, err := netip.ParseAddr(s); err != nil || a.Zone() != "" {
		return errNotIP
	}
	return nil
}

// validateCIDR accepts the address ranges netip.ParsePrefix reads.
func validateCIDR(v any) error {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if _, err := netip.ParsePrefix(s); err != nil {
		return errNotCIDR
	}
	return nil
}

// validatePortRange accepts a range of ports "<first>-<last>" that PortRange reads.
func validatePortRange(v any) error {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if _, _, ok := PortRange(s); !ok {
		return errNotPortRange
	}
	if !strings.Contains(s, "-") {
		return errQuotedPort
	}
	return nil
}

// PortRange is the first and last port of s: a port, or a range "<first>-<last>", each a number
// from 1 to 65535 in decimal without a sign or leading zeros, and the first no higher than the
// last. A port alone is a range of one port.
func PortRange(s string) (first, last int, ok bool) {
	a, b, isRange := strings.Cut(s, "-")
	if !isRange {
		b = a
	}
	first, okA := port(a)
	last, okB := port(b)
	if !okA || !okB || first > last {
		return 0, 0, false
	}
	return first, last, true
}

// port is s as a port: a number from 1 to 65535 written as strconv writes it.
func port(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}
