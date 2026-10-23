---
title: "Read config in a service"
description: "Grant a unit the config kinds it reads, and read their documents as files or with chalkcfg"
---

# Read config in a service

This page is for an image author whose service reads a node's config documents while it runs. At
the end a unit of the image is granted the `Peer` kind, reads its documents as JSON files or
through chalkcfg, and the image's evaluation checks each field the unit asks for. The page
continues the homelab's BGP extension; [Declare a config kind](declare-a-kind.md) declares its
`Peer` kind.

## Before you begin

You need an image module that declares the kind, and a unit of the image that reads it. A
service that reads a config file of its own format, such as bird's `bird.conf`, gets that file
rendered from the documents by an output instead; the last section covers it.

## Grant the unit its kinds

A grant names the kinds a unit reads, in `systemd.services.<unit>.chalkos.kinds`. This unit logs
the address of each of the node's neighbours when it starts:

```nix
systemd.services.bgp-peers = {
  wantedBy = [ "multi-user.target" ];
  chalkos.kinds = [ "Peer" ];
  serviceConfig = {
    Type = "oneshot";
    RemainAfterExit = true;
    ExecStart = pkgs.writeShellScript "bgp-peers" ''
      for address in $(${config.chalkos.kinds.Peer.get { path = "spec.address"; }}); do
        echo "peer $address"
      done
    '';
  };
};
```

The grant does three things for the unit:

- It puts chalkcfg on the unit's `PATH`.
- It loads, as the unit's credentials, the secret values that the `secretRef` fields of its
  kinds' documents name. [Pass secrets to a service](pass-secrets.md) covers them.
- It checks the kinds when the image is evaluated. A kind the image does not declare fails the
  evaluation and lists the kinds it does declare. A grant of `Secret` or `Addon` fails too: a unit
  never reads those documents.

bird's grant in `bgp.nix` exists for the second reason alone. bird reads its config from a
rendered file, and needs the grant only to receive the `Peer` passwords:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:grant"
```

`<unit>` is the attribute name under `systemd.services`, without `.service`.

## Documents are JSON files below /run/chalkos/config

chalkd writes each of the node's documents to `/run/chalkos/config/<group>/<Kind>/<name>.json`,
after the schemas' defaults are filled in. `config.chalkos.kinds.Peer.dir` names the kind's
directory, `/run/chalkos/config/bgp.example.org/Peer`, so a module never spells the path. For the
homelab's `router-b`, the file holds one line of JSON with sorted keys:

```json title="/run/chalkos/config/bgp.example.org/Peer/router-b.json"
{"apiVersion":"bgp.example.org/v1alpha1","kind":"Peer","metadata":{"name":"router-b"},"spec":{"address":"10.0.0.2","asn":64514,"password":null}}
```

`password` is `null` although the YAML leaves it out, because the field's default is `null`. Any
JSON tool reads the files.

chalkd writes the files at boot, from `chalkos-config.service`, which runs before
`sysinit.target`. Every ordinary service starts after that target, so the files exist when the
unit starts. After each `apply-config` to the running image, chalkd replaces the whole directory.

Every process on the node can read the files, granted or not. Secret values never appear below
`/run/chalkos/config`. `Secret` and `Addon` documents are left out of it.

## chalkcfg prints one field

`config.chalkos.kinds.<Kind>.get` builds a chalkcfg command that prints a field of the node's
documents. The unit above calls it with `path = "spec.address"`, which evaluates to:

```text
/nix/store/…-chalkcfg-0.1.0/bin/chalkcfg get Peer -o 'jsonpath={.spec.address}'
```

On a homelab node it prints `10.0.0.1` and `10.0.0.2`, one line per document in name order.
`get` takes three arguments, each optional:

- `name` picks one document, such as `name = "router-a"`.
- `path` prints one field. Fields are separated by dots. `*` or an index selects the elements of
  a list, and a key or `*` the values of an attribute set.
- `output` asks for `json`, `yaml`, `jsonpath=<expression>` or `go-template=<template>` in place
  of a `path`.

The evaluation checks `path` against the kind's options. A misspelled field fails the image's
build instead of printing nothing on the node, and the message lists the fields at that point:

```text
error: chalkos.kinds.Peer.get: the path "spec.asm" does not follow the kind's options: spec has no field asm; its fields are address, asn, password
```

The check covers the options, not the documents a node holds. On the node the command behaves as
follows:

- A kind with no documents on the node prints nothing and exits with status 0, so the unit also
  runs on a node without `Peer` documents.
- A `name` the node has no document of exits with status 1.
- A document that leaves the field out exits with status 1. Without `name`, one such document
  fails the whole command.
- A field set to null prints `null`.

`*` over an attribute set prints its values in no fixed order. A `go-template` output that ranges
over the keys prints them sorted. A path cannot hold a key that contains `.`, whitespace or one of
`'`, `\`, `@`, `$`, `,`, `[`, `]`, `{` and `}`; a `go-template` output reads such a key.

Run the command from a shell script, as `pkgs.writeShellScript` makes one, because systemd
expands `$` and `%` in `ExecStart=` itself. networkd's `Network`, `NetDev` and `Link` kinds have
no `get`, because their schemas come from systemd's tables when the image is built. A unit
granted them runs `chalkcfg get Network -o json` directly.
[`chalkcfg get`](../reference/cli/chalkcfg_get.md) lists every exit status.

## A grant restarts nothing

chalkd writes new documents after a delivery, but a running unit keeps what it read when it
started. An effect restarts or reloads the unit when its kinds change. The homelab's rack unit
reads its `Rack` once, so a changed `Rack` restarts it:

```nix
chalkos.effects.rack-location = lib.chalkos.restartOn { kinds = [ "Rack" ]; } [
  "rack-location.service"
];
```

The unit is a oneshot with `RemainAfterExit = true`, so it stays active and chalkd's restart runs
it again. The effect fires when a document of the kind is added, removed or changed, and when a
value of a `Secret` one of them references changes.
[Outputs and effects](../how-it-works/node/outputs-and-effects.md#what-is-an-effect) describes
when effects fire and in which order.

## A service that reads its own file format gets an output

Most daemons read a config file of their own format and know nothing of JSON documents. An output
renders such a file from the documents, and chalkd checks it before a delivery changes anything.
bird's `bird.conf` is an output of `bgp.nix`, rendered from the `Peer` documents through a
template. The homelab's `extensions/hardware.nix` renders its `Hardware` documents into an
environment file that its unit reads with `EnvironmentFile=`.

A unit that reads only its rendered output needs no grant, unless the output names a secret. A
renderer never sees secret values, so the unit receives them as credentials through a grant.
[Outputs and effects](../how-it-works/node/outputs-and-effects.md#what-is-an-output) describes
outputs.

## Check that it worked

Build the image. A grant of a kind the image lacks, or a `get` path the kind lacks, fails here and
names the option. Roll the image out with [Upgrade a cluster](../operate/upgrade-cluster.md),
then read the unit's journal:

```sh
chalkctl logs cp1 --unit=bgp-peers.service
```

It shows a line `peer 10.0.0.1` and a line `peer 10.0.0.2`. A unit that logs nothing found no
`Peer` documents on the node: check that the node's files hold them with `chalkctl validate`.

## What next

- [Pass secrets to a service](pass-secrets.md) gives bird the password `router-a` names.
- [Declare a config kind](declare-a-kind.md) adds fields to the kind.
- [Config and the inventory](../how-it-works/node/config.md) explains how the documents reach the
  node.
