package chalkcfg

// The long descriptions and examples of chalkcfg's commands, which its help and the generated
// reference pages show.

const rootLong = `chalkcfg prints the config documents of the node it runs on, as chalkd writes them below
/run/chalkos/config for the node's services, so a service or a shell on the node can read a value
of its config. It names a kind by its name alone, such as Rack, and knows the kinds the image
declares from the names of the schemas in /etc/chalkos/schemas. chalkcfg has no network access
and never prints Secrets: a service gets the values its kinds reference as systemd credentials.
Nor does it print Addon documents, whose Kubernetes objects chalkd keeps in the node's stored
config, readable by root alone, and applies on control planes.

In a NixOS module of an image, chalkos.kinds.<Kind>.get builds a chalkcfg command whose path the
image's evaluation checks against the kind's options.`

const rootExample = `  chalkcfg get
  chalkcfg get Rack rack -o jsonpath='{.spec.location}'`

const getLong = `Prints the node's documents of a kind, or the one with the given name. Without a kind, lists the
kinds the image declares, other than Secret and Addon, with their groups and the node's number
of documents of each.

Without --output, get prints the documents' names, one per line. -o json and -o yaml print the
document named, or else a list {"items": [...], "kind": "List"} of all documents of the kind.
-o jsonpath=<expression> and -o go-template=<template> print the result for each document on a
line of its own, in name order. Expressions follow kubectl's: {.spec.location}, {.spec.tags[*]}
or {.spec.labels['zone-a']}; an expression without braces is one path. Numbers compare as
numbers, as in {.spec.neighbors[?(@.asn==65001)].ip} or {{if eq .spec.port 179}}. A field or key
the document lacks is an error in both, also with a template's index.

A kind the image declares but the node has no documents of gives an empty result: no names, an
empty list, or no lines. get exits with 0 on success and with 2 for a usage error. It exits with
1 for a kind the image does not declare, for Secret and Addon, for a name the node has no document
of, for a kind in more than one group, for a jsonpath expression or template that fails to parse
or run, and for an error reading the config.`

const getExample = `  # The kinds the image declares and how many documents the node has of each.
  chalkcfg get

  # The names of the node's volumes, then one of them as YAML.
  chalkcfg get Volume
  chalkcfg get Volume data -o yaml

  # A field of every document of a kind, one line per document.
  chalkcfg get Node -o jsonpath='{.spec.type}'
  chalkcfg get Node -o go-template='{{range .spec.taints}}{{.key}} {{end}}'`
