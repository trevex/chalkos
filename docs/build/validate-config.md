---
title: "Validate config with a program"
description: "Refuse a node's config with a program of your image, for what the kind's schema and rules cannot check"
---

# Validate config with a program

This page is for an image author whose kind has constraints that a schema or a CEL rule cannot
express, or can express only at great cost. At the end a validator, a program of the image,
checks the node's documents before a delivery changes anything, and the operator sees which
document and field it refuses. The page continues the homelab's BGP extension,
`extensions/bgp.nix`: bird talks to a neighbour on one of the node's own links alone, so a `Peer`
whose address lies on none of them is refused.

A validator reads the documents of the kinds it names and prints the refusals it finds. chalkd
runs every validator after the change rules and before the outputs render. A refusal, or a
validator that fails, refuses the whole delivery, and the node keeps the config it runs.

## Before you begin

You need a kind of your own, as [Declare a config kind](declare-a-kind.md) declares `Peer`. Prefer
a rule where one does the job: [Guard changes with rules and facts](guard-changes.md) shows rules,
which chalkctl checks too, before it sends anything. A validator suits a check that needs a
program: a parser, a tool, data of the image's own, or a computation over many documents.

## Declare the validator

`chalkos.validators.<name>` declares a validator. Here is `bgp-peers`, as `extensions/bgp.nix`
declares it:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:validator"
```

The declaration sets three things:

- `inputs` names the kinds whose documents the validator reads: the `Peer` documents and the
  node's `Network` documents, which hold its addresses.
- `program` is the validator: here a jq program.
- `examples` is a directory of config files that the image build runs the validator on.

The name is lower-case letters, digits and dashes, as an output's.

## The validator reads one JSON array

chalkd runs the program as `<program> validate <name>`, here `validate bgp-peers`, and writes one
JSON array to its standard input. The array holds the documents of the kinds of `inputs`, sorted
by kind and then by name, with their schema defaults filled in. It holds no `Secret`, and a
`secretRef` field holds the reference, never the value. For cp1 of the homelab the array reads:

```json
[
  {"apiVersion": "networkd.chalkos.dev/v1alpha1", "kind": "Network", "metadata": {"name": "10-uplink"},
   "spec": {"match": {"name": "enp1s0"}, "network": {"address": ["10.0.0.11/24"], "gateway": "10.0.0.1"}}},
  {"apiVersion": "bgp.example.org/v1alpha1", "kind": "Peer", "metadata": {"name": "router-a"},
   "spec": {"address": "10.0.0.1", "asn": 64513, "password": {"secretRef": {"name": "bgp", "key": "password"}}}},
  {"apiVersion": "bgp.example.org/v1alpha1", "kind": "Peer", "metadata": {"name": "router-b"},
   "spec": {"address": "10.0.0.2", "asn": 64514, "password": null}}
]
```

## The validator prints a report of refusals

A validator refuses a config by printing one JSON object on standard output, the report of
refusals that a renderer prints too:

```json
{"refusals": [{"kind": "Peer", "name": "router-c", "path": "spec.address", "message": "10.0.1.1 lies on no subnet of the node's addresses, 10.0.0.11/24; bird reaches a neighbour on a link of its own alone"}]}
```

Each refusal names the document by `kind` and `name`, the field by its `path`, and says in
`message` what is wrong and how to fix it, on one line. [A renderer refuses a
config](render-config.md#a-renderer-refuses-a-config) lists the rules of each field.

The exit status tells whether the validator ran:

- Exit status 0 means it ran. A report without refusals, `{"refusals": []}`, or no output at all,
  accepts the config.
- Any other exit status fails the validator. The delivery is refused with
  `validator <name> failed (exit <n>); see the node's journal`, and any report it printed is
  ignored.
- Output that is not a report, or more than 64 KiB of it, fails the validator too.

A validator never sees a secret's value, so its messages cannot carry one. What it writes to
standard error goes to the node's journal alone, because it can echo the documents.

## A jq program validates

`lib.chalkos.validate.jq` turns a jq program into a validator: jq reads the array, and what the
program outputs is the report. `bgp-peers.jq` collects the subnets of the node's static IPv4
addresses and refuses each IPv4 `Peer` outside them:

```text title="bgp-peers.jq"
--8<-- "examples/homelab/extensions/bgp-peers.jq"
```

A node without a static IPv4 address passes, since its subnet is unknown. That keeps the
validator from refusing every peer of a node that takes its address by DHCP.

## Any program of the store validates

`program` also takes a package or a command line of the store. chalkd appends `validate` and the
validator's name to it, which a program that serves several validators reads to tell them apart.
A script made by `pkgs.writeShellScript` reads the array from standard input and prints the
report with `echo` or `jq`:

```nix
chalkos.validators.bgp-peers.program = pkgs.writeShellScript "bgp-peers" ''
  exec ${lib.getExe pkgs.jq} -c -f ${./bgp-peers.jq}
'';
```

A Go program, or one in any other language, works the same way: read the array, print the report,
exit 0.

## Examples run when the image is built

`examples` names a directory of YAML or JSON files, each one node's documents, checked against
the image's schemas. The image build runs the validator on each file and fails when it refuses
one or fails. `bgp.nix` runs `bgp-peers` on the examples of the output `bird-conf`, whose
`two-peers.yaml` holds both peers and the node's uplink `Network`. A jq program with a syntax
error then fails the build before any node gets it.

## Check that it worked

Build the image. The build runs each example, so a broken validator fails here and names the
validator and the example file.

Say `cluster/bgp.yaml` gains a `Peer` named `router-c` at 10.0.1.1, a neighbour behind a router.
`chalkctl apply-config cp1` checks the config as far as chalkctl can, then the node runs the
validator and refuses the delivery:

```console
$ chalkctl apply-config cp1
cp1: config 1 added, 0 removed, 0 changed
chalkctl: cp1: the config is refused: Peer/router-c: spec.address: 10.0.1.1 lies on no subnet of the node's addresses, 10.0.0.11/24; bird reaches a neighbour on a link of its own alone
```

The node changed nothing. `chalkctl apply-config --dry-run cp1` prints the same refusal without
a delivery. chalkctl cannot run the image's validators, so `chalkctl validate` accepts the
config.

A validator that fails names itself and its exit status. Each run is a transient unit,
`chalkos-render-<n>.service`, whose journal holds what the program wrote to standard error, and
the journal of `chalkd.service` names the unit beside the failure:

```sh
chalkctl logs cp1 --unit=chalkd.service
chalkctl logs cp1 --unit=chalkos-render-<n>.service
```

`chalkctl apply-config --staged` and `chalkctl upgrade` run the validators of the image the
config is for, from that image's store, in `chalkos-check.service`.

## The validator runs in a sandbox

chalkd runs every validator in the renderers' sandbox, as
[The renderer runs in a sandbox](render-config.md#the-renderer-runs-in-a-sandbox) describes. A
validator writes no output's files, so it may write no path but its private `/tmp` of 16 MiB. Like
a renderer, it opens no netlink socket. It must finish within 30 seconds, and the validators of a
delivery run one after another.

## What next

- [Render config files](render-config.md) renders `bird.conf` from the `Peer` documents.
- [Guard changes with rules and facts](guard-changes.md) refuses a change with a CEL rule, which
  chalkctl checks before it sends anything.
- [The config pipeline](../how-it-works/node/config-pipeline.md) shows where the validators run in
  a delivery.
