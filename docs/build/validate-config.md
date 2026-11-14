---
title: "Validate config with a program"
description: "Refuse a node's config with a program of your image, for what the kind's schema and rules cannot check"
---

# Validate config with a program

This page is for an image author whose kind has constraints that a schema or a CEL rule cannot
express, or can express only at great cost. At the end a validator, a program of the image,
checks the node's documents before a delivery changes anything, and the operator sees which
document and field it refuses. The page continues the homelab's BGP extension,
`extensions/bgp.nix`: bird binds each session to one of the node's interfaces, so a `Peer` that
names an interface the node does not configure is refused.

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

The declaration sets two things:

- `inputs` names the kinds whose documents the validator reads: the `Peer` and `Speaker`
  documents and the node's `Network` documents, which configure its interfaces.
- `program` is the validator: here a jq program.

The name is lower-case letters, digits and dashes, as an output's.

## The validator reads the documents by kind

chalkd runs the program as `<program> validate <name>`, here `validate bgp-peers`, and writes
one JSON object to its standard input: the input a renderer reads, as [The renderer reads one
JSON object](render-config.md#the-renderer-reads-one-json-object) describes. `documents` holds one
list per kind of `inputs`, each sorted by name, with the schema defaults filled in, and `node.name`
is the node's name. The object holds no `Secret`, and a `secretRef` field holds the reference,
never the value. For node1 of the homelab the object reads, trimmed to one `Network` and
indented here:

```json
{
  "documents": {
    "Network": [
      {"apiVersion": "networkd.chalkos.dev/v1alpha1", "kind": "Network", "metadata": {"name": "20-enp1s0"},
       "spec": {"match": {"name": "enp1s0"}, "network": {"ipv6AcceptRA": true}}}
    ],
    "Peer": [
      {"apiVersion": "bgp.example.org/v1alpha1", "kind": "Peer", "metadata": {"name": "router"},
       "spec": {"address": "fdc8:a1b2:c3d4:1::1", "asn": 65000, "bfd": true, "interfaces": ["enp1s0", "enp2s0"],
                "password": {"secretRef": {"name": "bgp", "key": "password"}}}}
    ],
    "Speaker": [
      {"apiVersion": "bgp.example.org/v1alpha1", "kind": "Speaker", "metadata": {"name": "bgp"},
       "spec": {"asn": 65001, "export": {"interfaces": ["node0"]}, "routerID": "0.0.0.11"}}
    ]
  },
  "node": {"name": "node1"}
}
```

## The validator prints a report of refusals

A validator refuses a config by printing one JSON object on standard output, the report of
refusals that a renderer prints too:

```json
{"refusals": [{"kind": "Peer", "name": "router", "path": "spec.interfaces[2]", "message": "enp3s0 has no Network document; bird binds the session to an interface the node configures"}]}
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

`chalkosLib.validate.jq` turns a jq program into a validator: jq reads the input, and what the
program outputs is the report. `bgp-peers.jq` collects the interface names of the node's `Network`
documents and refuses each interface of a `Peer` or the `Speaker` that is not among them:

```text title="bgp-peers.jq"
--8<-- "examples/homelab/extensions/bgp-peers.jq"
```

A section of a networkd document may be one object or a list of them, and a key one value or a
list, so the program reads `name` from every `[Match]` section, and splits a value that holds
several names. It compares the names as written. A glob such as `enp*` may or may not match
an interface, so the program takes it for no name, and a `Peer` on such an interface is refused.

The program also refuses `Peer` documents without a `Speaker`, which gives bird its AS number and
router ID. The template refuses the same config, as
[A renderer refuses a config](render-config.md#a-renderer-refuses-a-config) shows, but the
validators run before the renderers, so the operator sees this refusal. The homelab's tests run
the program on these cases and on `enp3s0` below.

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

## Tests run the validator before any node does

A test of `chalkos.tests` runs every validator on a config file, as chalkd does for a delivery,
and compares the refusals with those the test expects. A test that expects none expects the config
accepted. `nix flake check` runs the tests, so a jq program with a syntax error, or one that
refuses a config it should accept, fails there before any node gets the image. `bgp.nix` tests
`bgp-peers` on a config file per case below `tests/`: interfaces matched by lists, by a glob and by
names separated by spaces, a `Peer` without a `Speaker`, a `Speaker` that exports an interface
without a `Network` document and a node without `Peer` documents. It also tests
`bgp-examples/fabric.yaml`, with a node's `Speaker`, its `Peer` and the `Network` documents of its
ports and of `node0`, which the validator accepts.

## Check that it worked

Run the tests:

```sh
nix flake check
```

The homelab's test `bgp-unconfigured-peer-interface` gives the `Peer` a third interface, `enp3s0`,
without a `Network` document, and expects the refusal of the report above:

```nix
chalkos.tests.bgp-unconfigured-peer-interface = {
  config = ./tests/unconfigured-peer-interface.yaml;
  refusals = [
    {
      kind = "Peer";
      name = "router";
      path = "spec.interfaces[2]";
      message = "enp3s0 has no Network document; bird binds the session to an interface the node configures";
    }
  ];
};
```

A validator that fails, or whose refusals differ from those the test expects, fails the test with
a `FAIL` line of the refusals it expected and those it got, as
[Test an extension](test-an-extension.md#a-failing-test-shows-what-it-got) shows.

On a node, say the `Peer` of `bgp/fabric.yaml` gains the same interface, `enp3s0`, that the nodes
have no `Network` for. `chalkctl config apply node1` checks the config as far as chalkctl can,
then the node runs the validator and refuses the delivery:

```console
$ chalkctl config apply node1
node1: config 0 added, 0 removed, 1 changed
chalkctl: node1: the config is refused: Peer/router: spec.interfaces[2]: enp3s0 has no Network document; bird binds the session to an interface the node configures
```

The node changed nothing. `chalkctl config apply --dry-run node1` prints the same refusal without
a delivery. `chalkctl validate` accepts the config: chalkctl runs only the validators it links,
the router layer's `router` on router images, and an image's own validator runs on the node
alone.

A validator that fails names itself and its exit status. Each run is a transient unit,
`chalkos-render-<n>.service`, whose journal holds what the program wrote to standard error, and
the journal of `chalkd.service` names the unit beside the failure:

```sh
chalkctl logs node1 --unit=chalkd.service
chalkctl logs node1 --unit=chalkos-render-<n>.service
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

- [Test an extension](test-an-extension.md) tests the files, refusals and effects of a config.
- [Render config files](render-config.md) renders `bird.conf` from the `Speaker` and `Peer`
  documents.
- [Guard changes with rules and facts](guard-changes.md) refuses a change with a CEL rule, which
  chalkctl checks before it sends anything.
- [The config pipeline](../how-it-works/node/config-pipeline.md) shows where the validators run in
  a delivery.
