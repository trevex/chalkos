package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
)

// A refusal is a config the renderer refuses for what no schema or rule sees, about one field of
// one document: main prints it as a report of refusals on standard output, and chalkd reports it
// to the operator as it reports a rule's refusal. The message says what is wrong and how to fix
// it. It may quote any value of the documents, which hold no secret's value: a renderer reads a
// secretRef's reference alone, and chalkd hashes what else could carry one.
type refusal struct{ refusals.Refusal }

func (r *refusal) Error() string { return r.String() }

// refuse is the refusal of the field at path, such as spec.interfaces or spec.terms[0].match, of
// the document kind/name, with the message format gives. A refusal names its document and field
// where it is made, and within passes it on unchanged, so every output that shares a check refuses
// with the same refusal, which chalkd then reports once.
func refuse(kind, name, path, format string, args ...any) error {
	return &refusal{refusals.Refusal{Kind: kind, Name: name, Path: path, Message: fmt.Sprintf(format, args...)}}
}

// printRefusal prints r to w, standard output, as a report of refusals, with the control
// characters of its message, line breaks among them, as spaces, and reports whether it did. A
// refusal outside the format, which a template's refuse can make, is printed nowhere: main
// prints it on standard error, as any error, and the output fails.
func printRefusal(w io.Writer, r *refusal) bool {
	x := r.Refusal
	x.Message = strings.Map(func(c rune) rune {
		if c < ' ' || c == 0x7f {
			return ' '
		}
		return c
	}, x.Message)
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
