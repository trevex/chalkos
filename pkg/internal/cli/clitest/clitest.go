// Package clitest checks the cobra command trees of the chalkos programs in their tests.
package clitest

import (
	"testing"

	"github.com/spf13/cobra"
)

// CheckDescribed fails the test for each command of the tree of root, but a hidden one, that
// lacks a short or long description or examples, which its help and its reference page show.
func CheckDescribed(t testing.TB, root *cobra.Command) {
	t.Helper()
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if !cmd.Hidden && (cmd.Short == "" || cmd.Long == "" || cmd.Example == "") {
			t.Errorf("%s lacks a short or long description or examples", cmd.CommandPath())
		}
		for _, c := range cmd.Commands() {
			walk(c)
		}
	}
	walk(root)
}
