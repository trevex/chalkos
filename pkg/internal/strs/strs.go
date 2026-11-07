// Package strs joins values into the lists messages and flags show.
package strs

import (
	"fmt"
	"strings"
)

// Join joins the String of each of xs, separated by sep.
func Join[T fmt.Stringer](xs []T, sep string) string {
	list := make([]string, len(xs))
	for i, x := range xs {
		list[i] = x.String()
	}
	return strings.Join(list, sep)
}

// And lists names as prose, such as "a, b and c"; it is empty for no names.
func And(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
