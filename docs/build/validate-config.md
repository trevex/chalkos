---
title: "Validate config with a program"
description: "Refuse a node's config with a program of your image, for what the kind's schema and rules cannot check"
---

# Validate config with a program

This page is for an image author whose kind has constraints that a schema or a CEL rule cannot
express, or can express only at great cost. At the end a validator, a program of the image,
checks the node's documents before a delivery changes anything, and the operator sees which
document and field it refuses. The example reads the `Hardware` and `Rack` kinds of the
[extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions), and
refuses a `Hardware` document on a node that has no `Rack`.

A validator reads the documents of the kinds it names and prints the refusals it finds. chalkd
runs every validator after the change rules and before the outputs render. A refusal, or a
validator that fails, refuses the whole delivery, and the node keeps the config it runs.

See it in a full extension:
[Refuse an interface the node does not configure](../guides/bgp-extension.md#refuse-an-interface-the-node-does-not-configure)
of the BGP guide checks bird's interfaces against the node's `Network` documents.

## Before you begin

You need a kind of your own, as [Declare a config kind](declare-a-kind.md) declares one. Prefer
a rule where one does the job: [Guard changes with rules and facts](guard-changes.md) shows rules,
which chalkctl checks too, before it sends anything. A validator suits a check that needs a
program: a parser, a tool, data of the image's own, or a computation over many documents.

## Declare the validator

`chalkos.validators.<name>` declares a validator:

```nix
chalkos.validators.hardware-rack = {
  inputs = [ "Hardware" "Rack" ];
  program = chalkosLib.validate.jq ./hardware-rack.jq;
};
```

The declaration sets two things:

- `inputs` names the kinds whose documents the validator reads, here `Hardware` and `Rack`.
- `program` is the validator: here a jq program.

The name is lower-case letters, digits and dashes, as an output's.

## Read the validator's input

chalkd appends `validate hardware-rack` to the command, and writes one JSON object to the
program's standard input: the input a renderer reads, as
[Program input](../reference/extension-runtime.md#program-input) describes. On a node named
`example` that holds a `Hardware` and a `Rack`, the object reads, indented here:

```json
{
  "documents": {
    "Hardware": [
      {"apiVersion": "hardware.example.org/v1alpha1", "kind": "Hardware", "metadata": {"name": "hardware"},
       "spec": {"assetTag": null, "model": "SYS-E300-9D", "vendor": "Supermicro"}}
    ],
    "Rack": [
      {"apiVersion": "rack.example.org/v1alpha1", "kind": "Rack", "metadata": {"name": "rack"},
       "spec": {"location": "rack-a/u12"}}
    ]
  },
  "node": {"name": "example"}
}
```

## Print a report of refusals

A validator refuses a config by printing one JSON object on standard output, the report of
refusals that a renderer prints too:

```json
{"refusals": [{"kind": "Hardware", "name": "hardware", "path": "metadata.name", "message": "a node with a Hardware document needs a Rack document, which says where the server is mounted"}]}
```

Each refusal names the document by `kind` and `name`, the field by its `path`, and says in
`message` what is wrong and how to fix it. Any refusal refuses the delivery.
[Refusal report](../reference/extension-runtime.md#refusal-report) gives the rules of each field
and of the report, which a validator and a renderer share.

## Validate with a jq program

`chalkosLib.validate.jq` turns a jq program into a validator: jq reads the input, and what the
program outputs is the report. `hardware-rack.jq` refuses the `Hardware` document of a node
whose input holds no `Rack`:

```text title="hardware-rack.jq"
# Refuses Hardware documents on a node without a Rack document.
{
  refusals: [
    if (.documents.Hardware | length) > 0 and (.documents.Rack | length) == 0 then
      {
        kind: "Hardware",
        name: .documents.Hardware[0].metadata.name,
        path: "metadata.name",
        message: "a node with a Hardware document needs a Rack document, which says where the server is mounted"
      }
    else empty end
  ]
}
```

A template can refuse the same config, as
[Refuse a config from the renderer](render-config.md#refuse-a-config-from-the-renderer) shows,
but the validators run before the renderers, so the operator sees the validator's refusal.

## Validate with any program of the store

`program` also takes a package or a command line of the store, to which chalkd appends `validate`
and the validator's name. A script for execline runs jq on the input without a shell. Its `-P`
leaves the arguments chalkd appends unused, and jq reads the input from standard input and prints
the report:

```nix
chalkos.validators.hardware-rack.program = pkgs.writeScript "hardware-rack" ''
  #!${pkgs.execline}/bin/execlineb -P
  ${lib.getExe pkgs.jq} -c -f ${./hardware-rack.jq}
'';
```

A Go program, or one in any other language, works the same way: read the input, print the report,
exit 0. A program that serves as a renderer too reads the same input in both roles.

## Test the validator

A test of `chalkos.tests` runs every validator on a config file, as chalkd does for a delivery,
and compares the refusals with those the test expects. A test that expects none expects the config
accepted. `nix flake check` runs the tests, so a jq program with a syntax error, or one that
refuses a config it should accept, fails there before any node gets the image. Cover each branch
of the program: a config it accepts, and one per refusal. For `hardware-rack`, that is a node
with both kinds, one with a `Hardware` alone and one with neither.

## Check that it worked

Run the tests:

```sh
nix flake check
```

This test gives the node a `Hardware` without a `Rack`, and expects the refusal of the report
above:

```nix
chalkos.tests.hardware-without-rack = {
  config = ./tests/hardware-without-rack.yaml;
  refusals = [
    {
      kind = "Hardware";
      name = "hardware";
      path = "metadata.name";
      message = "a node with a Hardware document needs a Rack document, which says where the server is mounted";
    }
  ];
};
```

A validator that fails, or whose refusals differ from those the test expects, fails the test with
a `FAIL` line of the refusals it expected and those it got, as
[Test an extension](test-an-extension.md#a-failing-test-shows-what-it-got) shows.

On a node, `chalkctl config apply` checks the config as far as chalkctl can and prints what
changed. Then the node runs the validator and refuses the delivery, and chalkctl prints the
node's refusal after `the config is refused:`. The node changed nothing.
`chalkctl config apply --dry-run` prints the same refusal without a delivery.

`chalkctl validate` accepts such a config: chalkctl runs only the validators it links, the router
layer's `router` on router images, and an image's own validator runs on the node alone.

## If it fails

A validator that fails names itself, its exit status and the command that shows its journal.
Each run is a transient unit, `chalkos-render-<name>-<n>.service`, whose journal holds what the
program wrote to standard error:

```sh
chalkctl logs server1 --unit=chalkos-render-<name>-<n>.service
```

`chalkctl config apply` while an image is staged, with or without `--staged`, and
`chalkctl upgrade` run the validators of the image the config is for, from that image's store, in
`chalkos-check.service`. So does `chalkctl install` on a machine that runs the installer, once the
image's store is on the target disk.

A validator runs in the renderers' [sandbox](../reference/extension-runtime.md#sandbox), within
its limits of time and space.

## Built into chalkos

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
- [Filter and translate traffic](../router/firewall-and-nat.md#fix-a-refused-delivery)

## What next

- [Guard changes with rules and facts](guard-changes.md) refuses a change with a CEL rule, which
  chalkctl checks before it sends anything.
- [Test an extension](test-an-extension.md) tests the files, refusals and effects of a config.
- [Render config files](render-config.md) renders a service's config from the documents.
- [The config pipeline](../how-it-works/node/config-pipeline.md) shows where the validators run in
  a delivery.
