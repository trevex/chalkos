---
title: "Read config in a service"
description: "Grant a unit the config kinds it reads, and read their documents as files or with chalkcfg"
---

# Read config in a service

This page is for an image author whose service reads a node's config documents while it runs. At
the end a unit of the image is granted a kind, reads its documents as JSON files or through
chalkcfg, and the image's evaluation checks each field the unit asks for. The examples use the
`Rack` kind of the [extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions),
which records the rack and height unit a node is mounted in.

See it in a full extension: [Run bird](../guides/bgp-extension.md#run-bird) of the BGP guide
grants bird a kind for its passwords alone.

## Before you begin

You need an image module that declares the kind, as [Declare a config kind](declare-a-kind.md)
shows, and a unit of the image that reads it. A service that reads a config file of its own
format gets that file rendered from the documents by an output instead; the last section covers
it.

## Grant the unit its kinds

A grant names the kinds a unit reads, in `systemd.services.<unit>.chalkos.kinds`. The unit
`rack-location` of `rack.nix` prints the node's location when it starts:

```nix title="rack.nix"
--8<-- "examples/extensions/rack.nix:unit"
```

The grant does three things for the unit:

- It puts chalkcfg on the unit's `PATH`.
- It loads, as the unit's credentials, the secret values that the `secretRef` fields of its
  kinds' documents name. [Pass secrets to a service](pass-secrets.md) covers them.
- It checks the kinds when the image is evaluated. A kind the image does not declare fails the
  evaluation and lists the kinds it does declare. A grant of `Secret` or of a withheld kind, such as
  `Addon`, fails too: a unit never reads those documents.

A unit may need a grant for the second reason alone, such as a service that reads a rendered
file and receives a password beside it.

`<unit>` is the attribute name under `systemd.services`, without `.service`.

## Documents are JSON files below /run/chalkos/config

chalkd writes each of the node's documents to `/run/chalkos/config/<group>/<Kind>/<name>.json`,
after the schemas' defaults are filled in. `config.chalkos.kinds.Rack.dir` names the kind's
directory, `/run/chalkos/config/rack.example.org/Rack`, so a module never spells the path. Each
file holds one line of JSON with sorted keys. A `Hardware` document shows a default filled in:
for one that leaves out its asset tag, the file reads:

```json title="/run/chalkos/config/hardware.example.org/Hardware/hardware.json"
{"apiVersion":"hardware.example.org/v1alpha1","kind":"Hardware","metadata":{"name":"hardware"},"spec":{"assetTag":null,"model":"SYS-E300-9D","vendor":"Supermicro"}}
```

`assetTag` is `null` although the YAML leaves it out, because that is the field's default. Any
JSON tool reads the files.

chalkd writes the files at boot, from `chalkos-config.service`, which runs before
`sysinit.target`. Every ordinary service starts after that target, so the files exist when the
unit starts. After each `config apply` to the running image, chalkd replaces the whole directory.

Every process on the node can read the files, granted or not. Secret values never appear below
`/run/chalkos/config`. `Secret` documents and those of withheld kinds, such
as `Addon`, are left out of it.

## chalkcfg prints one field

`config.chalkos.kinds.<Kind>.get` builds a chalkcfg command that prints a field of the node's
documents. The unit above calls it with `name = "rack"` and `path = "spec.location"`, which
evaluates to:

```text
/nix/store/…-chalkcfg-0.1.0/bin/chalkcfg get Rack rack -o 'jsonpath={.spec.location}'
```

On `server1` of the example it prints `rack-a/u12`. Without `name`, a node with several
documents of a kind prints one line per document, in name order.
`get` takes three arguments, each optional:

- `name` picks one document, such as `name = "rack"`.
- `path` prints one field. Fields are separated by dots. `*` or an index selects the elements of
  a list, and a key or `*` the values of an attribute set.
- `output` asks for `json`, `yaml`, `jsonpath=<expression>` or `go-template=<template>` in place
  of a `path`.

The evaluation checks `path` against the kind's options. A misspelled field fails the image's
build instead of printing nothing on the node, and the message lists the fields at that point:

```text
error: chalkos.kinds.Rack.get: the path "spec.locaton" does not follow the kind's options: spec has no field locaton; its fields are location
```

The check covers the options, not the documents a node holds. On the node the command behaves as
follows:

- A kind with no documents on the node prints nothing and exits with status 0, so the unit also
  runs on a node without documents of the kind.
- A `name` the node has no document of exits with status 1.
- A document that leaves the field out exits with status 1. Without `name`, one such document
  fails the whole command.
- A field set to null prints `null`.

`*` over an attribute set prints its values in no fixed order. A `go-template` output that ranges
over the keys prints them sorted. A path cannot hold a key that contains `.`, whitespace or one of
`'`, `\`, `@`, `$`, `,`, `[`, `]`, `{` and `}`; a `go-template` output reads such a key.

A unit's `ExecStart=` takes the command as it is, as the rack unit above does, since systemd
reads its quotes as a shell would. systemd also expands `$` and `%` there, so a command that
holds either, such as one with a `go-template` output, needs each written twice, `$$` and `%%`.
[Keep an image shell-free](shell-free.md) shows what a unit does in place of a shell script.
networkd's `Network`, `NetDev` and `Link` kinds have
no `get`, because their schemas come from systemd's tables when the image is built. A unit
granted them runs `chalkcfg get Network -o json` directly.
[`chalkcfg get`](../reference/cli/chalkcfg_get.md) lists every exit status.

## A grant restarts nothing

chalkd writes new documents after a delivery, but a running unit keeps what it read when it
started. An effect restarts or reloads the unit when its kinds change. `rack-location` reads its
`Rack` once, so a changed `Rack` restarts it:

```nix
chalkos.effects.rack-location = chalkosLib.restartOn { kinds = [ "Rack" ]; } [
  "rack-location.service"
];
```

The unit is a oneshot with `RemainAfterExit = true`, so it stays active and chalkd's restart runs
it again. The effect fires when a document of the kind is added, removed or changed, and when a
value of a `Secret` one of them references changes.
[React to changes](react-to-changes.md) describes when effects fire and in which order.

## A service that reads its own file format gets an output

Most daemons read a config file of their own format and know nothing of JSON documents. An output
renders such a file from the documents, and chalkd checks it before a delivery changes anything.
The extensions example's `hardware.nix` renders its `Hardware` documents into an environment file
that its unit reads with `EnvironmentFile=`.

A unit that reads only its rendered output needs no grant, unless the output names a secret. A
renderer never sees secret values, so the unit receives them as credentials through a grant.
[Render config files](render-config.md) describes outputs.

## Check that it worked

Build the image. A grant of a kind the image lacks, or a `get` path the kind lacks, fails here and
names the option. Roll the image out with [Upgrade nodes](../operate/upgrade.md),
then read the unit's journal:

```sh
chalkctl logs server1 --unit=rack-location.service
```

It shows the line the unit printed, the node's location. The unit fails when the node holds no
`Rack` named `rack`, because `get` with a `name` then exits with status 1. Check that the node's
files hold one.

## What next

- [Render config files](render-config.md) renders a file from the documents for a service that
  reads its own format.
- [Pass secrets to a service](pass-secrets.md) gives a granted unit the values its documents
  reference.
- [Declare a config kind](declare-a-kind.md) adds fields to the kind.
- [Config and the inventory](../how-it-works/node/config.md) explains how the documents reach the
  node.
