package main

import (
	"io"

	"github.com/trevex/chalkos/pkg/refusals"
)

// A config the renderer refuses for what no schema or rule sees is a *refusals.Error about one
// field of one document: main prints it as a report of refusals on standard output, and chalkd
// reports it to the operator as it reports a rule's refusal. Its message may quote any value of
// the documents, which hold no secret's value: a renderer reads a secretRef's reference alone, and
// chalkd hashes what else could carry one.

// printRefusal prints r to w, standard output, as a report of refusals, with the runes of its
// message refusals.Unprintable reports, line breaks among them, as spaces, and reports whether it
// did. A refusal outside the format, which a template's refuse can make, is printed nowhere: main
// prints it on standard error, as any error, and the output fails.
func printRefusal(w io.Writer, r *refusals.Error) bool {
	x := r.Refusal
	x.Message = refusals.Printable(x.Message)
	if x.Check() != nil {
		return false
	}
	data, err := refusals.Encode([]refusals.Refusal{x})
	if err != nil {
		return false
	}
	_, err = w.Write(data)
	return err == nil
}
