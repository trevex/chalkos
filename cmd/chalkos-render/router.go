package main

import "github.com/trevex/chalkos/pkg/router/derive"

// checkInterfaces guards the interfaces a ruleset matches: an empty list, which would match no
// interface or, in an opening, leave the port open on every one, and a name or glob the schemas
// refuse, which strconv.Quote alone does not keep out of nftables's syntax.
func checkInterfaces(what string, l []string) error {
	if len(l) == 0 {
		return derive.InternalErrorf("%s names no interface", what)
	}
	for _, n := range l {
		if !derive.InterfaceName.MatchString(n) {
			return derive.InternalErrorf("%s: %q is no interface name or glob", what, n)
		}
	}
	return nil
}
