package main

import (
	"io"

	"github.com/trevex/chalkos/pkg/refusals"
)

// printRefusal prints r to w, standard output, as a report of refusals, with the runes
// refusals.Unprintable reports, line breaks among them, as spaces. The message may quote any value
// of the documents, which hold no secret's value: a renderer reads a secretRef's reference alone,
// and chalkd hashes what else could carry one. A refusal outside the format, which a template's
// refuse can make, is printed nowhere: main prints it on standard error, as any error, and the
// output fails.
func printRefusal(w io.Writer, r *refusals.Error) {
	x := r.Refusal
	x.Message = refusals.Printable(x.Message)
	if x.Check() != nil {
		return
	}
	if data, err := refusals.Encode([]refusals.Refusal{x}); err == nil {
		_, _ = w.Write(data)
	}
}
