package chalkcfg

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/internal/cli"
)

func getCommand(s store) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:     "get [<kind> [<name>]]",
		Short:   "Print the node's documents of a kind, or list the kinds",
		Long:    getLong,
		Example: getExample,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 2 {
				return cli.Usagef("get takes at most a kind and a name")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := s.resolve()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if format != "" {
					return cli.Usagef("--output needs a kind")
				}
				return s.listKinds(cmd.OutOrStdout())
			}
			out, err := parseOutput(format)
			if err != nil {
				return err
			}
			kind, name := args[0], ""
			if len(args) == 2 {
				name = args[1]
			}
			docs, err := s.documents(kind, name)
			if err != nil {
				return err
			}
			if len(docs) == 0 && out.format == "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "the node has no %s documents\n", kind)
			}
			// Nothing reaches stdout unless every document's output succeeded.
			var buf bytes.Buffer
			if err := out.write(&buf, docs, name != ""); err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(buf.Bytes())
			return err
		},
	}
	cmd.Flags().StringVarP(&format, "output", "o", "", "output format: json, yaml, jsonpath=<expression> or go-template=<template>; the documents' names without it")
	return cmd
}

// listKinds writes a table of the kinds the image declares, Secret and Addon excepted, with their
// groups and the node's number of documents of each.
func (s store) listKinds(w io.Writer) error {
	kinds, err := s.kinds()
	if err != nil {
		return err
	}
	maps.DeleteFunc(kinds, withheldKind)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tGROUP\tDOCUMENTS")
	for _, kind := range slices.Sorted(maps.Keys(kinds)) {
		k := kinds[kind]
		fmt.Fprintf(tw, "%s\t%s\t%d\n", kind, strings.Join(k.groups, ","), k.documents)
	}
	return tw.Flush()
}
