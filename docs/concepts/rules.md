---
title: "Change rules and facts"
description: "How an image's CEL rules decide which config changes a node accepts, and the node state they read"
---

# Change rules and facts

A [change rule](../reference/glossary.md#change-rule) is a CEL expression in an image's schemas
that every config the image accepts makes true. A rule decides whether a change of a node's
[config](../reference/glossary.md#config) can be made, or needs `chalkctl storage reset` or a
reinstall instead, and says so in its message. The image's own kinds and the kinds of its
[extensions](../reference/glossary.md#extension) declare rules the same way, in their NixOS
modules, so what a node accepts is part of the image and changes with an upgrade. A rule that
needs the node's state, such as what its disks hold, reads it from a
[fact](../reference/glossary.md#fact): the JSON of a file, or of a program of the image's store.

## Where are rules declared?

A field declares rules on its option, under `x-chalkos-rules`, and a kind declares rules over its
documents in [`chalkos.kinds.<name>.rules`](../reference/options.md#chalkoskindsrules). The
image build writes a field's rules into the field's property of the kind's JSON Schema and a kind's
rules into the schema's `x-chalkos-kind`. The homelab's `Rack` kind declares a rule on its only
field:

```nix
chalkos.kinds.Rack.spec.location =
  lib.mkOption {
    type = lib.types.nonEmptyStr;
    description = "Rack and height unit the node is mounted in.";
  }
  // {
    "x-chalkos-rules" = [
      {
        rule = "self == oldSelf || facts.rackSurvey == null || self == facts.rackSurvey.location";
        messageExpression = ''"the survey found the node at " + facts.rackSurvey.location + ", not at " + self + "; fix the location, or survey the node again"'';
      }
    ];
  };
```

A rule has these attributes:

| Attribute | Meaning |
| --- | --- |
| `rule` | CEL that is true for every config the node accepts. |
| `message` | What the node says when the rule is false: why, and what to do instead. |
| `messageExpression` | CEL that yields the message from the rule's variables. When it fails, the node says `message`. |
| `takes` | `refuse` (the default), `reset` or `reinstall`: what the change needs. |

`takes` sorts a refusal. A config whose rules fail is refused as a whole, and the node names the
most any failed rule takes first, then every failed rule's message:

```text
the config is refused: Volume extra: disk reference serial "chalk-missing" cannot be resolved: no disk matches serial "chalk-missing"; ...; fix the disk reference or plug in the disk it names
the config takes a reinstall of cp1: VAR: size 1G instead of filling its disk; only a reinstall of cp1 carries it out
the config takes chalkctl storage reset: Volume data: format xfs instead of ext4; reset it with chalkctl storage reset cp1 data
```

`refuse` is a change that cannot be made; its message says what to do instead. `reset` is a change
that [`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) carries out, and
`reinstall` one that only a reinstall of the node carries out. A field's message follows the kind,
the document and the field, as in `Kubernetes "kubernetes" spec.ipFamilies: ...`; a kind's
message names what it is about itself.

## Which variables does a rule see?

Every rule is compared with the running image's config, the slot the node boots now, which is the
baseline also when the config is for another image, as with an upgrade.

| Variable | Rules | Value |
| --- | --- | --- |
| `self` | a field's | The field's value in the delivered document, with the schema's defaults filled in. |
| `oldSelf` | a field's | The field's value in the running config's document of the same kind and name; for a kind a node holds once, its document whatever its name. |
| `documents` | a kind's | The delivered documents of the kind by name. |
| `oldDocuments` | a kind's | The running config's documents of the kind by name. |
| `config` | every | The whole delivered config by apiVersion, kind and name: `config["storage.chalkos.dev/v1alpha1"].Volume.data`. |
| `oldConfig` | every | The running config, in the same shape. |
| `facts.<name>` | every | The fact of that name. |

A field's rule that reads `oldSelf` runs only where the running config holds the field; one that
does not runs wherever the delivered config holds it. In every variable, and in what a command
fact reads, each value of a `Secret` and each `data` and `stringData` value of a Kubernetes Secret
an `Addon` applies is its SHA-256, as [GetConfig](../reference/api.md#method-getconfig) shows them,
so a rule can tell whether a secret changed without reading it. Maps iterate in the order of their keys, so a list built from the names of
`documents` is sorted.

## What is a fact?

A fact is node state a rule reads as `facts.<name>`, declared in
[`chalkos.facts`](../reference/options.md#chalkosfacts) and written to `/etc/chalkos/facts.json`.
It is one of:

- a file, `{ file = "/state/kubernetes/share.json"; fields = [ "kind" ]; }`, whose JSON is the
  fact, and null while the file is missing. `fields`, when given, keeps only those keys of the
  file's JSON object, so a rule sees nothing else of the file. A file that cannot be read, holds
  no JSON or, with `fields`, no JSON object refuses the delivery;
- a command, `{ command = "${pkg}/bin/tool facts"; }`, a program of the image's store with its
  arguments. It reads `{"config": ..., "oldConfig": ...}` as JSON on standard input, in the shape
  rules see, and prints the fact as JSON. A command that lists `inputs = [ "fallbackSecret" ];`
  also reads the fallback secret delivered with the config; no other command does.

chalkd loads a fact when a rule first reads it and keeps it for the rest of the delivery, which it
handles under the config lock: a file is read once, and a command runs once for each config it
reads. A check of the staged image's slot, which a storage change of the running image's config
takes, gives a command the staged slot's config to read. A rule that does not reach a fact, as one
whose first operand of `||` holds, loads none. chalkd runs a command through
`systemd-run --pipe --wait` as a transient unit with these properties:

| Property | Effect |
| --- | --- |
| `ProtectSystem=strict`, `ReadOnlyPaths=-/state` | The whole file system, STATE among it, is read-only. Only `/run/cryptsetup`, cryptsetup's lock directory, stays writable, for the passphrase test. |
| `ProtectHome=yes`, `PrivateTmp=yes` | Home directories are hidden, and `/tmp` is the unit's own. |
| `ProtectKernelTunables=yes`, `ProtectKernelModules=yes`, `ProtectControlGroups=yes` | Kernel tunables and control groups are read-only, and the unit loads no kernel module. |
| `PrivateNetwork=yes`, `RestrictAddressFamilies=AF_UNIX AF_ALG` | No network, and no socket but Unix sockets and the kernel crypto API's. |
| `NoNewPrivileges=yes` | No program gains privileges it does not have. |

The unit runs as root with root's capabilities, and devices stay accessible: resolving disks and
testing a passphrase read block devices, and cryptsetup may set up a temporary device-mapper
mapping for a cipher the kernel crypto API lacks. A command that exits with an error, takes longer
than 30 seconds or prints more than 1 MiB, or whose output is no JSON, refuses the delivery:

```text
cannot check: fact disks: /nix/store/...-chalkos-storage-facts did not finish within 30s
```

Facts are the running image's, since they read this node, whichever image the config is for. A
fact the running image does not declare reads as null, as a fact that a newer image's rules read
does during the upgrade to that image, so a rule treats null as unknown. An install has no
running config: rules of fields that read `oldSelf` do not run, and the rules of kinds see no
`oldDocuments`. Every fact reads as null at an install, in place or from the installer, so an
install tried again does not read what an earlier attempt left on STATE, and runs no command.

## Where do rules run?

chalkd evaluates every rule, with its facts, each time it writes a config slot, before anything
changes:

- [`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) for the running image, with
  the running image's rules;
- `chalkctl apply-config --staged` and [upgrades](upgrades.md), with the rules of the image the
  slot is for;
- [`chalkctl install`](../reference/cli/chalkctl_install.md), with the rules of the image it
  installs.

[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) evaluates the storage kinds'
rules against the storage STATE records without the volume it resets, which it recreates like a
new one: a reset is refused when the config needs more than recreating that volume. A config for
the running image that adds to the machine's storage is checked against the staged image's config
too, as [Config and the inventory](config.md#which-changes-does-a-node-accept) describes.

chalkctl evaluates the rules that read no facts, since the facts are the node's.
[`chalkctl validate`](../reference/cli/chalkctl_validate.md) has no running config to compare with,
so it runs the rules of kinds and the rules of fields that do not read `oldSelf`. `chalkctl
apply-config` asks the node for its config through GetConfig and runs those rules against it too
before it delivers anything, so a change a rule refuses fails without reaching the node.

## Which functions can a rule call?

Rules have CEL's standard functions, the lists, sets, strings and bindings (`cel.bind`)
extensions of cel-go, and chalkos's address and size functions:

| Function | Result |
| --- | --- |
| `cidr(s)` | The address range `s`, with its address masked, so `cidr("fd00:0::/108") == cidr("fd00::/108")` and `cidr("10.244.1.0/16") == cidr("10.244.0.0/16")`. |
| `<cidr>.ip(n)` | The address `n` places after the range's first, which must lie in the range: `cidr("10.96.0.0/12").ip(10)` is `10.96.0.10`. |
| `<cidr>.prefix()` | The prefix length, an int. |
| `<cidr>.contains(<ip>)` | Whether the range holds the address. |
| `ip(s)` | The address `s`, equal to every spelling of it. |
| `<ip>.family()` | `ipv4` or `ipv6`, as `ipFamilies` names them. |
| `string(<cidr>)`, `string(<ip>)` | The canonical spelling: `fd00::/108`, `fd00::a`. |
| `bytes(s)` | A size such as `1G` or `512M` in bytes, an int, with the suffixes powers of 1024 as in repart. It takes the place of CEL's conversion of a string to bytes. |
| `isSize(s)` | Whether `bytes(s)` reads `s`: digits with an optional K, M, G, T or P suffix, below 8 EiB. A recorded section an older image wrote can hold a size that `bytes` does not read. |

## How is a rule checked when the image is built?

The image build runs `chalkos-rules check` over the image's schemas and facts, and fails on any
rule that does not parse or type-check, on a rule that reads a fact the image does not declare and
on a command fact that is no program of the store. It also refuses a rule whose estimated cost
exceeds 10,000,000, estimated with 256 elements in every list, map and string, which a loop over
a kind's documents inside another loop over them fits and a third nested loop does not. A rule is
stopped at a cost of 1,000,000 on the config it meets.

## Which rules do the built-in kinds declare?

| Kind | Rule | Takes |
| --- | --- | --- |
| `Node` | The type stays the one the node's Kubernetes share is for, read from `facts.share`. | refuse |
| `Kubernetes` | `ipFamilies`, and the pod ranges, service ranges and DNS addresses of the families both configs have, stay once the node holds a share. Ranges and addresses compare as what they denote; an unset DNS address is the 10th of its service range. | refuse |
| `SystemDisk` | VAR keeps its format, encryption and size; a larger VAR, or one that fills the disk, is accepted. | reinstall |
| `Volume` | A volume keeps its disk, and a changed disk reference finds the disk it lives on. | reinstall |
| `Volume` | A volume keeps its format and encryption, also the encryption it inherits from the `SystemDisk`, and does not shrink or take a fixed size where it filled its disk. | reset |
| `Volume` | A disk reference resolves, and the disk of a volume the config creates or changes is plugged in. | refuse |
| `Volume` | No volume the machine records is left out. | reinstall |
| `Volume` | A new encrypted volume comes with the fallback secret, which opens the node's fallback keyslot. | refuse |

They read these facts:

| Fact | Source | Value |
| --- | --- | --- |
| `share` | `/state/kubernetes/share.json` | The kind of the node's Kubernetes share, the share's keys left out; declared on images with Kubernetes. |
| `recorded` | `/state/storage/storage.json` | The storage section STATE records. |
| `disks` | `chalkos-storage facts disks` | For each volume with a disk of its own, whether its disk is pinned and plugged in, and what a changed reference finds. |
| `fallback` | `chalkos-storage facts fallback` | Whether a fallback secret was sent, and whether it opens the node's fallback keyslot. |

## Limits

- A rule binds `self` to a path through the properties of objects, so it cannot be declared on the
  items of a list or the values of a map; it is declared on the list or the map, which a kind's
  rule or a field's rule compares whole. Lists are not matched item by item by a key.
- Rules compare the config with the running image's config alone. The config of another image's
  slot is visible to no rule.
- A command fact runs on the node that evaluates the rules, with the running image's program, so a
  fact that a newer image adds is null until that image runs.
- A message can show a fact's content in an error the node returns. A file fact's `fields` keep
  what the rules do not need of the file, such as the share's private keys, out of the fact. An
  error of a rule that cannot be evaluated leaves out what it quotes, and is cut to 512 bytes, but
  can still show a key that a map lacks.

## Related pages

- [Config and the inventory](config.md) for the changes a node accepts and the slots rules compare.
- [Storage and encryption](storage.md) for what the storage rules protect.
- [Customise the image](../guides/customise-image.md#add-a-rule-and-a-fact-to-a-kind) for a rule and
  a fact on an extension's kind.
- [`chalkos.kinds`](../reference/options.md#chalkoskinds) and
  [`chalkos.facts`](../reference/options.md#chalkosfacts) for the options.
