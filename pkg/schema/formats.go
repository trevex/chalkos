package schema

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/trevex/chalkos/pkg/imageref"
)

// formats are the values of "format" that the kinds' schemas assert, besides JSON Schema's own:
// an address, an address range, a range of ports, an image reference and an image reference whose
// tag is a version. Each accepts a subset of what the rules' ip(), cidr() and portRange() and the
// renderers read, stricter where they differ (ip refuses the zones ip() reads), so that a value
// the schema accepts parses everywhere; the image references are those imageref.Parse reads. A
// JSON Schema tool that knows none of them takes them as annotations.
var formats = []*jsonschema.Format{
	{Name: "ip", Validate: validateIP},
	{Name: "cidr", Validate: validateCIDR},
	{Name: "port-range", Validate: validatePortRange},
	{Name: "image-reference", Validate: validateImageReference},
	{Name: "versioned-image-reference", Validate: validateVersionedImageReference},
}

// The errors of the formats, which follow "'<value>' is not valid <format>: ".
var (
	errNotIP        = errors.New("an address is an IPv4 address of four numbers from 0 to 255 without leading zeros, or an IPv6 address without a zone")
	errNotCIDR      = errors.New("an address range is an address, a slash and a prefix length of at most 32 for IPv4 or 128 for IPv6")
	errNotPortRange = errors.New(`a range of ports is "<first>-<last>", each from 1 to 65535, and the first no higher than the last`)
	errQuotedPort   = errors.New(errNotPortRange.Error() + "; a single port is a number, unquoted")
	errNotImage     = errors.New("an image reference names its registry, its repository and a tag, a digest or both, as registry.example/app:v1.2.3@sha256:<digest>")
	errNotVersioned = errors.New("this image names its version in its tag, as registry.example/app:v1.2.3, optionally followed by @sha256:<digest>; a reference by digest alone or with another tag has no version")
)

// validateImageReference accepts the references imageref.Parse reads.
func validateImageReference(v any) error {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if _, err := imageref.Parse(s); err != nil {
		return notImage(err)
	}
	return nil
}

// notImage is the error of a value imageref.Parse refuses: what Parse found wrong, and what a
// reference names.
func notImage(err error) error {
	if cause := errors.Unwrap(err); cause != nil {
		err = cause
	}
	return fmt.Errorf("%w; %w", err, errNotImage)
}

// validateVersionedImageReference accepts the references imageref.Parse reads whose tag is a
// version.
func validateVersionedImageReference(v any) error {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	r, err := imageref.Parse(s)
	if err != nil {
		return notImage(err)
	}
	if _, err := r.Version(); err != nil {
		return errNotVersioned
	}
	return nil
}

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

// FormatNames are the values of "format" the kinds' schemas assert besides JSON Schema's own.
func FormatNames() []string {
	names := make([]string, len(formats))
	for i, f := range formats {
		names[i] = f.Name
	}
	return names
}
