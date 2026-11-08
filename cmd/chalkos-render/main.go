// Command chalkos-render is the renderer of the outputs the helpers of lib.chalkos.render build,
// and of the built-in outputs networkd, router-networkd, router-sysctl, router-nftables, frr, jool,
// unbound, kea and dhcp-relay. chalkd runs it in the sandbox of an output: it reads the output's
// input as JSON on standard input, {"node": {"name": …}, "documents": {"<Kind>": [<document>, …]}},
// and writes files into $OUT, the only path it may write. A router image's build runs it once more
// for the image's fallback ruleset. It is the program of the validator router too, which chalkd
// runs as chalkos-render validate router in a validator's sandbox: it reads the same input on
// standard input and prints its refusals on standard output.
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

template executes the Go text/template TEMPLATE on the input and writes the result to $OUT/FILE;
the template has the functions join, quote, json, default and refuse. documents writes each
document of the kinds KIND as $OUT/<Kind>/<name>.json. networkd writes each Network, NetDev
and Link document as the networkd unit $OUT/<name>.network, .netdev or .link, with the section
and key names of the kinds' schemas in the directory SCHEMAS. router-networkd writes them as
networkd does, with each VRF document's device as $OUT/00-vrf-<name>.netdev and .network and
VRF= in the Network units of the links it holds. router-sysctl writes the kernel settings of
the Router document as $OUT/90-chalkos-router.conf. router-nftables writes the firewall of the
documents of the router kinds as the nftables table inet chalkos in $OUT/ruleset.nft, which
accepts chalkd's TCP port PORT; router-nftables-fallback writes the table of a node without
a rendered ruleset, which forwards nothing. frr writes FRR's config of the BGP documents as
$OUT/frr.conf. jool writes Jool's config of each NAT64 document as $OUT/<name>.json. unbound
writes Unbound's config of the Resolver document as $OUT/unbound.conf, with the root's trust
anchor in the file TRUST-ANCHOR and the zone ipv4only.arpa in the file ZONE-FILE. kea writes the
config of Kea's DHCPv4 and DHCPv6 servers of each VRF whose links DHCPSubnet documents serve as
$OUT/kea-dhcp4.json and $OUT/kea-dhcp6.json for the default VRF and $OUT/kea-dhcp4-<vrf>.json
and $OUT/kea-dhcp6-<vrf>.json for a VRF; a DHCPv6 server whose subnets delegate prefixes runs
the program PD-HOOK on their leases. dhcp-relay writes the config of dnsmasq's DHCP relay of
each VRF whose links DHCPRelay documents relay as $OUT/dnsmasq-relay.conf for the default VRF and
$OUT/dnsmasq-relay-<vrf>.conf for a VRF. Every command prints a config it refuses as
{"refusals": [{"kind": …, "name": …, "path": …, "message": …}]} on standard output, and fails.
validate router reads the input of the router's kinds as a renderer does, prints the report of
the refusals of the validator router on standard output, one of no refusals for a config it
accepts, and succeeds; it fails on input it cannot read.`

// main runs the renderer. A config it refuses, a *refusals.Error, it prints as a report of
// refusals on standard output for chalkd to report, and on standard error too, for the journal
// and for a run by hand; any other error, an internal one or one of its command line, goes to
// standard error alone, where chalkd shows it to no one but the node's journal, since it can echo
// the documents. Either fails the renderer, so that a build that runs it, as the router's fallback
// ruleset's does, fails too. A validator prints its report itself and fails on an error alone.
func main() {
	args := os.Args[1:]
	if err := run(args, os.Stdin, os.Stdout, os.Getenv("OUT")); err != nil {
		// A validator's error is its failure, whose standard output chalkd reads no report from.
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
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var in map[string]any
	if err := dec.Decode(&in); err != nil {
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
