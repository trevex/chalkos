package chalkcfg

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
	"k8s.io/client-go/util/jsonpath"
)

// output is a parsed --output: the format, and the expression or template of jsonpath and
// go-template.
type output struct {
	format   string
	jsonpath *jsonpath.JSONPath
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
		jp := jsonpath.New("output")
		if err := jp.Parse(expr); err != nil {
			return output{}, fmt.Errorf("the jsonpath expression %s: %w", expr, err)
		}
		return output{format: "jsonpath", jsonpath: jp}, nil
	case strings.HasPrefix(s, "go-template="):
		t, err := template.New("output").Option("missingkey=error").Parse(strings.TrimPrefix(s, "go-template="))
		if err != nil {
			return output{}, fmt.Errorf("the template: %w", err)
		}
		return output{format: "go-template", template: t}, nil
	}
	return output{}, fmt.Errorf("%w: unknown output format %q: use json, yaml, jsonpath=<expression> or go-template=<template>", errUsage, s)
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
		var err error
		if o.jsonpath != nil {
			err = o.jsonpath.Execute(w, d.object)
		} else {
			err = o.template.Execute(w, d.object)
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
		tag := "!!int"
		if strings.ContainsAny(string(v), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: string(v)}
	}
	return v
}
