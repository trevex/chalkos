package storage

import (
	"fmt"
	"regexp"
	"strconv"
)

var (
	sizeRE       = regexp.MustCompile(`^([0-9]+)([KMGTP]?)$`)
	comparisonRE = regexp.MustCompile(`^(<=|>=|<|>|=)? *([0-9]+[KMGTP]?)$`)
	unitShift    = map[string]uint{"": 0, "K": 10, "M": 20, "G": 30, "T": 40, "P": 50}
)

// ParseSize parses a size such as 512M or 2T in bytes; suffixes are powers of 1024, as in repart.
func ParseSize(s string) (uint64, error) {
	m := sizeRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	shift := unitShift[m[2]]
	if n > (^uint64(0))>>shift {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return n << shift, nil
}

// matchSize reports whether size satisfies a comparison such as ">= 1T"; no operator means equal.
func matchSize(comparison string, size uint64) (bool, error) {
	m := comparisonRE.FindStringSubmatch(comparison)
	if m == nil {
		return false, fmt.Errorf("invalid size comparison %q", comparison)
	}
	want, err := ParseSize(m[2])
	if err != nil {
		return false, err
	}
	switch m[1] {
	case "<":
		return size < want, nil
	case "<=":
		return size <= want, nil
	case ">":
		return size > want, nil
	case ">=":
		return size >= want, nil
	default:
		return size == want, nil
	}
}
