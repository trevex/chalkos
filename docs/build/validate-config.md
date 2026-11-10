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

## The validator reads the documents by kind

chalkd runs the program as `<program> validate <name>`, here `validate bgp-peers`, and writes
one JSON object to its standard input: the input a renderer reads, as [The renderer reads one
JSON object](render-config.md#the-renderer-reads-one-json-object) describes. `documents` holds one
list per kind of `inputs`, each sorted by name, with the schema defaults filled in, and `node.name`
is the node's name. The object holds no `Secret`, and a `secretRef` field holds the reference,
never the value. For cp1 of the homelab the object reads, indented here:

```json
{
  "documents": {
    "Network": [
      {"apiVersion": "networkd.chalkos.dev/v1alpha1", "kind": "Network", "metadata": {"name": "10-uplink"},
       "spec": {"match": {"name": "enp1s0"}, "network": {"address": ["10.0.0.11/24"], "gateway": "10.0.0.1"}}}
    ],
    "Peer": [
      {"apiVersion": "bgp.example.org/v1alpha1", "kind": "Peer", "metadata": {"name": "router-a"},
       "spec": {"address": "10.0.0.1", "asn": 64513, "password": {"secretRef": {"name": "bgp", "key": "password"}}}},
      {"apiVersion": "bgp.example.org/v1alpha1", "kind": "Peer", "metadata": {"name": "router-b"},
       "spec": {"address": "10.0.0.2", "asn": 64514, "password": null}}
    ]
  },
  "node": {"name": "cp1"}
}
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

A validator reads its input and reports refusals as a renderer does, so the same rules decide:

- Any refusal refuses the delivery, whether the validator then exits with 0 or not.
- A report without refusals, `{"refusals": []}`, or no output at all, refuses nothing. The
  validator then passes when it exits with 0.
- Any other exit status fails the validator. The delivery is refused with
  `validator <name>: its program failed (exit <n>); see the node's journal`.
- Output that is not a report, or more than 64 KiB of it, fails the validator too.

A validator never sees a secret's value, so its messages cannot carry one. What it writes to
standard error goes to the node's journal alone, because it can echo the documents.

## A jq program validates

`lib.chalkos.validate.jq` turns a jq program into a validator: jq reads the input, and what the
program outputs is the report. `bgp-peers.jq` collects the subnets of the node's static IPv4
addresses and refuses each IPv4 `Peer` outside them:

```text title="bgp-peers.jq"
--8<-- "examples/homelab/extensions/bgp-peers.jq"
```

A section of a networkd document may be one object or a list of them, and a key one value or a
list, so the program reads `address` from every `[Network]` section and every `[Address]`
section, `spec.address`. An address without a prefix length counts as a host address, /32.

The subnet of a link that takes its address by DHCP is unknown. So when any `Network` of the node
has no static IPv4 address, or enables DHCPv4, the validator accepts every `Peer`; otherwise it
would refuse a peer on that link. The check `example-homelab-bgp-peers` runs the program on these
cases and on `router-c` below.

## Any program of the store validates

`program` also takes a package or a command line of the store. chalkd appends `validate` and the
validator's name to it, which a program that serves several validators reads to tell them apart.
A script made by `pkgs.writeShellScript` reads the input from standard input and prints the
report with `echo` or `jq`:

```nix
chalkos.validators.bgp-peers.program = pkgs.writeShellScript "bgp-peers" ''
  exec ${lib.getExe pkgs.jq} -c -f ${./bgp-peers.jq}
'';
```

A Go program, or one in any other language, works the same way: read the input, print the report,
exit 0. A program that serves as a renderer too reads the same input in both roles.

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
`chalkctl config apply cp1` checks the config as far as chalkctl can, then the node runs the
validator and refuses the delivery:

```console
$ chalkctl config apply cp1
cp1: config 1 added, 0 removed, 0 changed
chalkctl: cp1: the config is refused: Peer/router-c: spec.address: 10.0.1.1 lies on no subnet of the node's addresses, 10.0.0.11/24; bird reaches a neighbour on a link of its own alone
```

The node changed nothing. `chalkctl config apply --dry-run cp1` prints the same refusal without
a delivery. `chalkctl validate` accepts the config: chalkctl runs only the validators it links,
the router layer's `router` on router images, and an image's own validator runs on the node
alone.

A validator that fails names itself and its exit status. Each run is a transient unit,
`chalkos-render-<n>.service`, whose journal holds what the program wrote to standard error, and
the journal of `chalkd.service` names the unit beside the failure:

```sh
chalkctl logs cp1 --unit=chalkd.service
chalkctl logs cp1 --unit=chalkos-render-<n>.service
```

`chalkctl config apply` while an image is staged, with or without `--staged`, and
`chalkctl upgrade` run the validators of the image the config is for, from that image's store, in
`chalkos-check.service`. So does `chalkctl install` on a machine that runs the installer, once the
image's store is on the target disk. A boot runs no validator, since every config slot passed the
validators of its image before it was written.

## The validator runs in a sandbox

chalkd runs every validator in the renderers' sandbox, as
[The renderer runs in a sandbox](render-config.md#the-renderer-runs-in-a-sandbox) describes. A
validator writes no output's files, so it may write no path but its private `/tmp` of 16 MiB. Like
a renderer, it opens no netlink socket. It must finish within 30 seconds, and the validators of a
delivery run one after another.

## Router images run the validator router

A router image declares the [validator `router`](../reference/builtins.md#validator-router). It
checks what the router kinds' documents and the `Network` documents mean together: which links
networkd puts in which VRF, and those links against the firewall's zones, the NAT rules, the
management interfaces, the policy routes' `in`, the BGP peers, the NAT64s, the resolvers and the
DHCP subnets and relays. chalkctl links the same code, so `chalkctl validate` and
`chalkctl config apply` run it before they send anything, and the node runs it again as
`chalkos-render validate router`. It refuses the VRFs' links first. The checks that read the
links run once those pass, and report every fault they find at once.

The router guide lists its messages and their fixes, beside those of the renderers, which run on
the node alone:

- [Separate routing with VRFs](../router/vrfs.md#fix-a-refused-delivery)
- [Route with BGP](../router/bgp.md#fix-a-refused-delivery)
- [Route traffic by policy](../router/policy-routing.md#fix-a-refused-delivery)
- [Translate to IPv4 and resolve names](../router/nat64-and-dns.md#fix-a-refused-delivery)
- [Lease addresses with DHCP](../router/dhcp.md#fix-a-refused-delivery)
- [Filter and translate traffic](../router/firewall-and-nat.md#a-refusal-names-the-document)

## What next

- [Render config files](render-config.md) renders `bird.conf` from the `Peer` documents.
- [Guard changes with rules and facts](guard-changes.md) refuses a change with a CEL rule, which
  chalkctl checks before it sends anything.
- [The config pipeline](../how-it-works/node/config-pipeline.md) shows where the validators run in
  a delivery.
