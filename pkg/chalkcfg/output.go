package chalkcfg

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
	"k8s.io/client-go/util/jsonpath"

	"github.com/trevex/chalkos/pkg/internal/cli"
)

// output is a parsed --output: the format, and the expression or template of jsonpath and
// go-template.
type output struct {
	format string
	// expr is the jsonpath expression, in braces. write parses it again for each document, because
	// a parsed JSONPath keeps the state of its ranges from one Execute to the next.
	expr     string
	template *template.Template
}

func parseOutput(s string) (output, error) {
	switch {
	case s == "", s == "json", s == "yaml":
		return output{format: s}, nil
	case strings.HasPrefix(s, "jsonpath="):
		expr := strings.TrimPrefix(s, "jsonpath=")
		// As in kubectl, an expression without braces is a single path.
		if !strings.Contains(expr, "{") {
			expr = "{" + expr + "}"
		}
		if _, err := parseJSONPath(expr); err != nil {
			return output{}, err
		}
		return output{format: "jsonpath", expr: expr}, nil
	case strings.HasPrefix(s, "go-template="):
		t, err := template.New("output").Option("missingkey=error").Funcs(template.FuncMap{"index": index}).Parse(strings.TrimPrefix(s, "go-template="))
		if err != nil {
			return output{}, fmt.Errorf("the template: %w", err)
		}
		return output{format: "go-template", template: t}, nil
	}
	return output{}, cli.Usagef("unknown output format %q: use json, yaml, jsonpath=<expression> or go-template=<template>", s)
}

func parseJSONPath(expr string) (*jsonpath.JSONPath, error) {
	jp := jsonpath.New("output")
	if err := jp.Parse(expr); err != nil {
		return nil, fmt.Errorf("the jsonpath expression %s: %w", expr, err)
	}
	return jp, nil
}

// write writes docs to w: the document itself as JSON or YAML when one was asked for by name, a
// list of them otherwise; names alone without a format; and each document's jsonpath or template
// result on a line of its own.
func (o output) write(w io.Writer, docs []document, named bool) error {
	switch o.format {
	case "":
		for _, d := range docs {
			if _, err := fmt.Fprintln(w, d.name); err != nil {
				return err
			}
		}
		return nil
	case "json", "yaml":
		var v any
		if named {
			v = docs[0].object
		} else {
			items := make([]any, len(docs))
			for i, d := range docs {
				items[i] = d.object
			}
			// As kubectl lists objects, so jq '.items[]' reads both. It has no apiVersion: v1 is
			// Kubernetes's core group.
			v = map[string]any{"kind": "List", "items": items}
		}
		if o.format == "json" {
			data, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return err
			}
			_, err = w.Write(append(data, '\n'))
			return err
		}
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(yamlValue(v)); err != nil {
			return err
		}
		return enc.Close()
	}
	for _, d := range docs {
		// Filters and templates compare numbers, which a json.Number, a string type, is not.
		obj := numbers(d.object)
		var err error
		if o.format == "jsonpath" {
			var jp *jsonpath.JSONPath
			if jp, err = parseJSONPath(o.expr); err == nil {
				err = jp.Execute(w, obj)
			}
		} else {
			err = o.template.Execute(w, obj)
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", d.kind, d.name, err)
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

// numbers returns v with each json.Number as an int64 where it is one and a float64 otherwise,
// as kubectl decodes objects for jsonpath and templates.
func numbers(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = numbers(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = numbers(e)
		}
		return out
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return v
}

// index is the template function index, but a key a map lacks is an error, as missingkey=error
// makes it for fields, where the builtin index returns the map's zero value.
func index(item any, keys ...any) (any, error) {
	v := reflect.ValueOf(item)
	for _, key := range keys {
		for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		k := reflect.ValueOf(key)
		switch v.Kind() {
		case reflect.Map:
			if !k.IsValid() || !k.Type().AssignableTo(v.Type().Key()) {
				return nil, fmt.Errorf("cannot index a map with %v", key)
			}
			e := v.MapIndex(k)
			if !e.IsValid() {
				return nil, fmt.Errorf("map has no entry for key %q", fmt.Sprint(key))
			}
			v = e
		case reflect.Slice, reflect.Array, reflect.String:
			var inRange bool
			switch {
			case k.CanInt():
				inRange = k.Int() >= 0 && k.Int() < int64(v.Len())
			case k.CanUint():
				inRange = k.Uint() < uint64(v.Len())
			default:
				return nil, fmt.Errorf("cannot index a list with %v", key)
			}
			if !inRange {
				return nil, fmt.Errorf("index out of range: %v", key)
			}
			v = v.Index(int(k.Convert(reflect.TypeFor[int]()).Int()))
		default:
			return nil, fmt.Errorf("cannot index %v", item)
		}
	}
	if !v.IsValid() {
		return nil, nil
	}
	return v.Interface(), nil
}

// yamlValue returns v with each json.Number as a YAML number: the encoder would write a
// json.Number, a string type, as a quoted string.
func yamlValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = yamlValue(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = yamlValue(e)
		}
		return out
	case json.Number:
		s := string(v)
		if !strings.ContainsAny(s, ".eE") {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: s}
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: yamlFloat(s)}
	}
	return v
}

// yamlFloat writes a JSON number with an exponent so that it has a point and a signed exponent,
// as YAML 1.1 needs to read it as a float rather than a string: 1e3 becomes 1000.0, 1E-7
// 1.0e-07. A number without an exponent stays as written.
func yamlFloat(s string) string {
	if !strings.ContainsAny(s, "eE") {
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	out := strconv.FormatFloat(f, 'g', -1, 64)
	if strings.Contains(out, ".") {
		return out
	}
	if mant, exp, ok := strings.Cut(out, "e"); ok {
		return mant + ".0e" + exp
	}
	return out + ".0"
}
