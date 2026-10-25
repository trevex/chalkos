// Command chalkos-render is the renderer of the outputs the helpers of lib.chalkos.render build,
// and of the built-in outputs networkd, router-sysctl and router-nftables. chalkd runs it in the
// sandbox of an output: it reads the output's input as JSON on standard input, {"node": {"name":
// …}, "documents": {"<Kind>": [<document>, …]}}, and writes files into $OUT, the only path it may
// write. A router image's build runs it once more for the image's fallback ruleset.
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
)

const usage = `usage: chalkos-render template TEMPLATE FILE
       chalkos-render documents KIND...
       chalkos-render networkd SCHEMAS
       chalkos-render router-sysctl
       chalkos-render router-nftables PORT
       chalkos-render router-nftables-fallback PORT

template executes the Go text/template TEMPLATE on the input and writes the result to $OUT/FILE;
the template has the functions join, quote, json and default. documents writes each document of
the kinds KIND as $OUT/<Kind>/<name>.json. networkd writes each Network, NetDev and Link document
as the networkd unit $OUT/<name>.network, .netdev or .link, with the section and key names of the
kinds' schemas in the directory SCHEMAS. router-sysctl writes the kernel settings of the Router
document as $OUT/90-chalkos-router.conf. router-nftables writes the firewall of the documents of
the router kinds as the nftables table inet chalkos in $OUT/ruleset.nft, which accepts chalkd's TCP
port PORT; router-nftables-fallback writes the table of a node without a rendered ruleset, which
forwards nothing.`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Getenv("OUT")); err != nil {
		fmt.Fprintln(os.Stderr, "chalkos-render: "+err.Error())
		if errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, usage)
		}
		os.Exit(1)
	}
}

// errUsage is a command line chalkos-render does not take; main prints the usage after it.
var errUsage = errors.New("unknown command line")

func run(args []string, stdin io.Reader, out string) error {
	if len(args) == 0 {
		return errUsage
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
	case args[0] == "router-sysctl" && len(args) == 1:
		return renderRouterSysctl(in, out)
	case args[0] == "router-nftables" && len(args) == 2:
		return renderRouterNftables(args[1], false, in, out)
	case args[0] == "router-nftables-fallback" && len(args) == 2:
		return renderRouterNftables(args[1], true, in, out)
	}
	return errUsage
}

// readInput reads the input with its numbers exact, as documents hold them.
func readInput(r io.Reader) (map[string]any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var in map[string]any
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("read the input: %w", err)
	}
	return in, nil
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
	byKind, _ := in["documents"].(map[string]any)
	for _, kind := range kinds {
		docs, ok := byKind[kind].([]any)
		if !ok {
			return fmt.Errorf("the output's inputs hold no kind %s", kind)
		}
		for _, d := range docs {
			obj, _ := d.(map[string]any)
			name, err := documentName(kind, obj)
			if err != nil {
				return err
			}
			b, err := json.MarshalIndent(obj, "", "  ")
			if err != nil {
				return err
			}
			if err := writeFile(out, filepath.Join(kind, name+".json"), append(b, '\n')); err != nil {
				return err
			}
		}
	}
	return nil
}

// documentName is the name of doc, a document of kind, which names a file of its own.
func documentName(kind string, doc map[string]any) (string, error) {
	meta, _ := doc["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	if name == "" || strings.ContainsAny(name, "/\x00") || name == "." || name == ".." {
		return "", fmt.Errorf("a %s document has no name to name its file by", kind)
	}
	return name, nil
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
