// Command chalkos-render renders chalkd's built-in outputs and those of chalkosLib.render's
// helpers, and is the program of the validator router. chalkd runs it in an output's or a
// validator's sandbox with the input as JSON on standard input, {"node": {"name": …},
// "documents": {"<Kind>": [<document>, …]}}; a renderer writes into $OUT, the only path it may
// write. A router image's build runs it for the fallback ruleset.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
)

const usage = `usage: chalkos-render template TEMPLATE FILE
       chalkos-render documents KIND...
       chalkos-render networkd SCHEMAS
       chalkos-render router-networkd SCHEMAS
       chalkos-render router-sysctl
       chalkos-render router-nftables PORT
       chalkos-render router-nftables-fallback PORT
       chalkos-render frr
       chalkos-render jool
       chalkos-render unbound TRUST-ANCHOR ZONE-FILE
       chalkos-render kea PD-HOOK
       chalkos-render dhcp-relay
       chalkos-render validate router

Each renderer reads the output's input on standard input and writes into $OUT; a config it
refuses it prints as {"refusals": [...]} on standard output, and fails. validate router prints
the validator's report on standard output, one of no refusals for a config it accepts, and
succeeds; it fails on input it cannot read.`

// main prints a refused config, a *refusals.Error, as a report on standard output for chalkd, and
// every error on standard error, which only the journal sees, since it can echo the documents.
// Either fails the renderer, and so a build that runs it. A validator prints its report itself; on
// an error it prints none and fails, and chalkd reports the exit status.
func main() {
	args := os.Args[1:]
	if err := run(args, os.Stdin, os.Stdout, os.Getenv("OUT")); err != nil {
		if r, ok := errors.AsType[*refusals.Error](err); ok && args[0] != "validate" {
			printRefusal(os.Stdout, r)
		}
		fmt.Fprintln(os.Stderr, "chalkos-render: "+err.Error())
		if errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, usage)
		}
		os.Exit(1)
	}
}

// errUsage is a command line chalkos-render does not take; main prints the usage after it.
var errUsage = errors.New("unknown command line")

// run runs the command line args on stdin: a renderer writes into out, the output's directory, and
// returns a config it refuses as a *refusals.Error; a validator prints its report on stdout.
func run(args []string, stdin io.Reader, stdout io.Writer, out string) error {
	if len(args) == 0 {
		return errUsage
	}
	if args[0] == "validate" {
		if len(args) != 2 {
			return errUsage
		}
		return validate(args[1], stdin, stdout)
	}
	if out == "" {
		return errors.New("$OUT names no directory to write")
	}
	in, err := readInput(stdin)
	if err != nil {
		return err
	}
	switch {
	case args[0] == "template" && len(args) == 3:
		return renderTemplate(args[1], args[2], in, out)
	case args[0] == "documents" && len(args) > 1:
		return writeDocuments(args[1:], in, out)
	case args[0] == "networkd" && len(args) == 2:
		return renderNetworkd(args[1], in, out)
	case args[0] == "router-networkd" && len(args) == 2:
		return renderRouterNetworkd(args[1], in, out)
	case args[0] == "router-sysctl" && len(args) == 1:
		return renderRouterSysctl(in, out)
	case args[0] == "router-nftables" && len(args) == 2:
		return renderRouterNftables(args[1], false, in, out)
	case args[0] == "router-nftables-fallback" && len(args) == 2:
		return renderRouterNftables(args[1], true, in, out)
	case args[0] == "frr" && len(args) == 1:
		return renderFRR(in, out)
	case args[0] == "jool" && len(args) == 1:
		return renderJool(in, out)
	case args[0] == "unbound" && len(args) == 3:
		return renderUnbound(args[1], args[2], in, out)
	case args[0] == "kea" && len(args) == 2:
		return renderKea(args[1], in, out)
	case args[0] == "dhcp-relay" && len(args) == 1:
		return renderDHCPRelay(in, out)
	}
	return errUsage
}

// validators are the validators chalkos-render runs, by name: each takes what a validator reads
// on standard input and returns its refusals, as the outputs.ValidateFunc that chalkctl links
// does; chalkos-render takes the standard library alone and so names no such type.
var validators = map[string]func(documents []byte) ([]refusals.Refusal, error){
	"router": derive.Validate,
}

// validate runs the validator name on the input on stdin and prints its report on stdout,
// one of no refusals for a config it accepts, with the runes of each message that
// refusals.Unprintable reports as spaces, so that a message that quotes one stays a refusal. An
// error is the validator's failure, which chalkd reports without the report.
func validate(name string, stdin io.Reader, stdout io.Writer) error {
	v, ok := validators[name]
	if !ok {
		return errUsage
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read the input: %w", err)
	}
	rs, err := v(data)
	if err != nil {
		return fmt.Errorf("validator %s: %w", name, err)
	}
	for i := range rs {
		rs[i].Message = refusals.Printable(rs[i].Message)
	}
	report, err := refusals.Encode(rs)
	if err != nil {
		return err
	}
	_, err = stdout.Write(report)
	return err
}

// readInput reads the input with its numbers exact, as documents hold them.
func readInput(r io.Reader) (map[string]any, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read the input: %w", err)
	}
	var in map[string]any
	if err := jsonfile.DecodeExact(data, &in); err != nil {
		return nil, fmt.Errorf("read the input: %w", err)
	}
	return in, nil
}

// documents are the documents of the input in, by kind.
func documents(in map[string]any) map[string]any {
	byKind, _ := in["documents"].(map[string]any)
	return byKind
}

// funcs are the template's functions beside text/template's own.
var funcs = template.FuncMap{
	// join joins the values of a list, each as it prints, with sep.
	"join": func(sep string, list []any) string {
		parts := make([]string, len(list))
		for i, v := range list {
			parts[i] = fmt.Sprint(v)
		}
		return strings.Join(parts, sep)
	},
	// quote is a value as a double-quoted string with Go's escapes.
	"quote": func(v any) string { return strconv.Quote(fmt.Sprint(v)) },
	// json is a value as JSON.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	// nilEmpty is v, or nothing to print when v is nil; every action that prints ends with it.
	"nilEmpty": func(v any) any {
		if v == nil {
			return ""
		}
		return v
	},
	// default is v, or d when v is missing, null, empty or false.
	"default": func(d, v any) any {
		if empty(v) {
			return d
		}
		return v
	},
	// refuse refuses the config: it stops the template with the refusal of the field at path of
	// the document kind/name, which chalkd reports as it reports a rule's.
	"refuse": func(kind, name, path, message string) (string, error) {
		return "", refusals.Errorf(kind, name, path, "%s", message)
	},
}

func empty(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.String, reflect.Slice, reflect.Map:
		return r.Len() == 0
	case reflect.Bool:
		return !r.Bool()
	}
	return false
}

// renderTemplate writes the template at path, executed on in, to out/file. A key the documents
// lack, or that holds null, is nil, which default replaces and which prints as nothing; a key
// below one the documents lack fails the template.
func renderTemplate(path, file string, in map[string]any, out string) error {
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tmpl, err := template.New(filepath.Base(path)).Funcs(funcs).Option("missingkey=zero").Parse(string(text))
	if err != nil {
		return err
	}
	for _, t := range tmpl.Templates() {
		if t.Tree != nil {
			printNilEmpty(t.Tree.Root)
		}
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, in); err != nil {
		return err
	}
	return writeFile(out, file, buf.Bytes())
}

// printNilEmpty ends the pipeline of every action below n that prints with nilEmpty, since
// text/template prints a nil value as "<no value>".
func printNilEmpty(n parse.Node) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			printNilEmpty(c)
		}
	case *parse.ActionNode:
		// An action that declares a variable prints nothing.
		if len(n.Pipe.Decl) == 0 {
			n.Pipe.Cmds = append(n.Pipe.Cmds, &parse.CommandNode{
				NodeType: parse.NodeCommand,
				Pos:      n.Pos,
				Args:     []parse.Node{parse.NewIdentifier("nilEmpty").SetPos(n.Pos)},
			})
		}
	case *parse.IfNode:
		printNilEmpty(n.List)
		printNilEmpty(n.ElseList)
	case *parse.RangeNode:
		printNilEmpty(n.List)
		printNilEmpty(n.ElseList)
	case *parse.WithNode:
		printNilEmpty(n.List)
		printNilEmpty(n.ElseList)
	}
}

// writeDocuments writes each document of kinds in in as out/<Kind>/<name>.json.
func writeDocuments(kinds []string, in map[string]any, out string) error {
	byKind := documents(in)
	for _, kind := range kinds {
		docs, ok := byKind[kind].([]any)
		if !ok {
			return fmt.Errorf("the output's inputs hold no kind %s", kind)
		}
		for _, d := range docs {
			obj, _ := d.(map[string]any)
			name, err := derive.DocumentName(kind, obj)
			if err != nil {
				return err
			}
			if err := writeJSON(out, filepath.Join(kind, name+".json"), obj); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeJSON writes v as indented JSON to the file rel below out.
func writeJSON(out, rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(out, rel, append(data, '\n'))
}

// number is n as a JSON number.
func number[T ~uint16 | ~uint32](n T) json.Number {
	return json.Number(strconv.FormatUint(uint64(n), 10))
}

// writeFile writes data to out/rel, which must stay below out.
func writeFile(out, rel string, data []byte) error {
	if !filepath.IsLocal(rel) {
		return fmt.Errorf("%q is no file name below $OUT", rel)
	}
	path := filepath.Join(out, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
