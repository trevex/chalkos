---
title: "chalkcfg"
description: "Print the config documents of the node chalkcfg runs on"
---

# chalkcfg

Print the config documents of the node chalkcfg runs on

## Synopsis

chalkcfg prints the config documents of the node it runs on, as chalkd writes them below
/run/chalkos/config for the node's services, so a service or a shell on the node can read a value
of its config. It names a kind by its name alone, such as Rack, and knows the kinds the image
declares from the names of the schemas in /etc/chalkos/schemas. chalkcfg has no network access
and never prints Secrets: a service gets the values its kinds reference as systemd credentials.
Nor does it print Addon documents, whose Kubernetes objects chalkd keeps in the node's stored
config, readable by root alone, and applies on control planes.

In a role module, chalkos.kinds.&lt;Kind>.get builds a chalkcfg command whose path the image's
evaluation checks against the kind's options.

## Examples

```
  chalkcfg get
  chalkcfg get Rack rack -o jsonpath='{.spec.location}'
```

## Options

```
  -h, --help   help for chalkcfg
```

## SEE ALSO

* [chalkcfg get](chalkcfg_get.md)	 - Print the node's documents of a kind, or list the kinds

