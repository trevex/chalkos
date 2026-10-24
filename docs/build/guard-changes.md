---
title: "Guard changes with rules and facts"
description: "Refuse a config change a node cannot make, with CEL rules on your kind and facts about the node"
---

# Guard changes with rules and facts

This page is for an image author whose kind has values that must not change freely once a node
runs them. At the end the kind carries change rules, chalkctl and chalkd refuse a change that
breaks one, and the refusal tells the operator what to do instead. The page uses the homelab's
two extensions: the `Peer` kind of `extensions/bgp.nix` and the `Rack` kind of
`extensions/rack.nix`.

A change rule is a CEL expression that every config the node accepts makes true. The rules are
part of the image's schemas, so what a node accepts changes with an upgrade. A rule that needs
node state, such as what a survey found, reads it from a fact: the JSON of a file or of a program.

## Before you begin

You need a kind of your own, as [Declare a config kind](declare-a-kind.md) declares one. Rules
on a field need `lib.chalkos.mkOption`, which that page introduces. CEL is the
[Common Expression Language](https://cel.dev/); its syntax resembles that of C and JavaScript.

## A field rule keeps a Peer's AS number

A BGP neighbour's AS number is part of who it is: a new number is a new neighbour. The `asn`
field of `Peer` declares a rule that keeps it:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:rule"
```

In a field's rule, `self` is the field's value in the delivered document and `oldSelf` its value
in the document of the same name that the node runs. `self == oldSelf` holds when the number stays
the same. A rule that reads `oldSelf` runs only where the node's running config holds the field,
so a new `Peer` takes any number.

Say `cluster/bgp.yaml` changes `router-b`'s `asn` from 64514 to 64600. `chalkctl apply-config cp1`
fetches the node's config, prints what changed, runs the rule against the node's config, and
stops before it sends anything:

```console
$ chalkctl apply-config cp1
cp1: config 0 added, 0 removed, 1 changed
chalkctl: cp1: the config is refused: Peer "router-b" spec.asn: the AS number stays 64514; add a Peer of another name for 64600
```

The error names the node, then the refusal. A field rule's message starts with the kind, the
document's name and the field. The rest comes from `messageExpression`, which builds the message
from the same variables as the rule.

## A rule has four attributes

| Attribute | Meaning |
| --- | --- |
| `rule` | CEL that is true for every config the node accepts. |
| `message` | What the node says when the rule is false: why, and what to do instead. |
| `messageExpression` | CEL that yields the message. When it fails, the node says `message`, or names the rule's kind or field without one. |
| `takes` | What the refused change needs instead: `refuse`, the default, `reset` or `reinstall`. |

A rule without either message says `the config does not meet a rule of` and the kind or field
that declares it, such as `the config does not meet a rule of Peer spec.asn`.

## A refusal starts with what the change takes

A config whose rules fail is refused as a whole. The refusal starts with the most that any failed
rule takes, then lists every failed rule's message, separated by `;`:

| `takes` | The refusal starts with | Meaning |
| --- | --- | --- |
| `refuse` | `the config is refused:` | The change cannot be made. The message says what to do instead. |
| `reinstall` | `the config takes a reinstall of <node>:` | Only a reinstall of the node carries the change out. |
| `reset` | `the config takes chalkctl storage reset:` | `chalkctl storage reset` carries the change out. |

Your own kinds mostly use `refuse`. The storage kinds use `reset` and `reinstall`, because a
volume's new format is possible once the volume is recreated.
[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) recreates one volume.

## A kind rule sees all of a kind's documents

A rule over all documents of a kind goes into
[`chalkos.kinds.<name>.rules`](../reference/extension-api.md#chalkoskindsrules). Here `documents` maps
the names of the delivered `Peer` documents to the documents:

```nix
chalkos.kinds.Peer.rules = [
  {
    rule = "size(documents) <= 2";
    message = "a node has two BGP neighbours at most";
  }
];
```

With a third `Peer` in `cluster/bgp.yaml`, `chalkctl validate` refuses both nodes:

```console
$ chalkctl validate
chalkctl: the config of 2 nodes is invalid:
node cp1: the config is refused: a node has two BGP neighbours at most
node w1: the config is refused: a node has two BGP neighbours at most
```

A kind's message names nothing by itself, so it says which kind it is about.

## Rules read these variables

Every rule compares the delivered config with the config of the slot the node boots now. That
config is the baseline also when the delivered config is for another image, as in an upgrade.

| Variable | Rules | Value |
| --- | --- | --- |
| `self` | a field's | The field's value in the delivered document, with the schema's defaults filled in. |
| `oldSelf` | a field's | The field's value in the running config's document of the same kind and name; for a kind a node holds once, its document whatever its name. |
| `documents` | a kind's | The delivered documents of the kind, by name. |
| `oldDocuments` | a kind's | The running config's documents of the kind, by name. |
| `config` | every | The whole delivered config by apiVersion, kind and name: `config["bgp.example.org/v1alpha1"].Peer["router-a"]`. |
| `oldConfig` | every | The running config, in the same shape. |
| `node.name` | every | The node's name in the inventory, which its node certificate carries. |
| `facts.<name>` | every | The fact of that name. |

Every value of a `Secret` is its SHA-256 in these variables, so a rule can tell that a secret
changed without reading it. Maps iterate in the order of their keys, so a list built from the
names of `documents` is sorted.

A rule binds `self` to a path through the properties of objects. It cannot sit on the items of a
list or the values of a map. Declare it on the list or the map, which the rule then compares
whole. Lists are not matched item by item by a key.

!!! note "Kubernetes: an Addon's Secret values are hashes too"

    On images with Kubernetes, each `data` and `stringData` value of a Kubernetes Secret that an
    `Addon` applies is its SHA-256 in these variables as well.

## Facts give a rule node state

A fact is node state that a rule reads as `facts.<name>`. The homelab's `Rack` kind refuses a
location that differs from the one a rack survey wrote to the node:

```nix title="extensions/rack.nix"
--8<-- "examples/homelab/extensions/rack.nix"
```

[`chalkos.facts`](../reference/extension-api.md#chalkosfacts) declares the fact `rackSurvey`: the JSON
of `/var/lib/rack-survey/location.json`, or null while the file is missing. The rule does not
read `oldSelf`, so it checks every `Rack` a node is delivered, its first one too.

Say the survey file on `cp1` holds `{"location": "rack-b/u3"}`, because the node moved, while
`nodes/cp1.yaml` still says `rack-a/u12`. The rule reads a fact, so chalkctl cannot check it and
the node refuses the delivery. chalkctl prints the node's error after `chalkctl:` and the error's
code. The message part of it is:

```text
the config is refused: Rack "rack" spec.location: the survey found the node at rack-b/u3, not at rack-a/u12; fix the location, or survey the node again
```

Set the location the survey found where the survey is right, or survey the node again where the
document is.

A fact is a file or a command:

- A file fact, `{ file = "/var/lib/rack-survey/location.json"; }`, is the file's JSON, and null
  while the file is missing. `fields = [ "location" ]` keeps only those keys of the file's JSON
  object, so a rule and its message see nothing else of the file. `hashOthers = true` keeps the
  object's other keys too, each as the hex SHA-256 of its value, so a rule sees that a value
  changed but not what it holds. A file that cannot be read or holds no JSON, or no JSON object
  with `fields` or `hashOthers`, refuses the delivery.
- A command fact, `{ command = "${pkg}/bin/tool facts"; }`, is a program of the image's store with
  its arguments. It reads `{"config": ..., "oldConfig": ...}` on standard input, in the shape that
  rules see, and prints the fact as JSON. A command with `inputs = [ "fallbackSecret" ];` also
  reads the fallback secret delivered with the config. No other command reads it.

chalkd loads a fact when a rule first reads it, and keeps it for the rest of the delivery. A file
is read once. A command runs once for each config it reads: a storage change of the running
image's config also checks the staged image's slot, and gives the command that slot's config to
read. A rule that does not reach a fact, such as one whose first operand of `||` holds, loads
none. A fact is read by its name alone: `facts["rackSurvey"]`, `has(facts.rackSurvey)` or `facts`
by itself fails the build.

## Null means the node does not know

Facts are the running image's, because they describe this node, whichever image the config is
for. Several cases give null, so a rule treats null as unknown:

- A file fact whose file is missing.
- A fact that the running image does not declare, as when a newer image's rules read a new fact
  during the upgrade to it.
- Every fact at an install, in place or from the installer, so a repeated install does not read
  what an earlier attempt left on STATE.

An install also has no running config: rules of fields that read `oldSelf` do not run, and the
rules of kinds see no `oldDocuments`. The `Rack` rule therefore accepts any location at install.

## chalkctl and chalkd both run the rules

chalkd evaluates every rule, with its facts, each time it writes a config slot and before it
changes anything:

- `chalkctl apply-config` for the running image, with the running image's rules;
- `chalkctl apply-config --staged` and upgrades, with the rules of the image the slot is for;
- `chalkctl install`, with the rules of the image it installs.

chalkd compiles every image's rules with its own CEL library. An upgrade therefore compiles the
new image's rules before that image's chalkd runs, and refuses a slot whose rules call a function
the running chalkd lacks.

chalkctl evaluates the rules that read no facts, because the facts are the node's. `chalkctl
validate` has no running config, so it runs the rules of kinds and the rules of fields that do not
read `oldSelf`. `chalkctl apply-config` fetches the node's config first and runs those rules
against it too, so a change that the `asn` rule refuses fails without reaching the node. The
`Rack` rule reads a fact, so it runs on the node alone.

`chalkctl storage reset` evaluates the storage kinds' rules against STATE's storage records
without the volume it resets, which it recreates like a new one.

## Rules call CEL's functions and chalkos's

Rules have CEL's standard functions, the lists, sets, strings and bindings (`cel.bind`)
extensions of cel-go, and chalkos's address and size functions:

| Function | Result |
| --- | --- |
| `cidr(s)` | The address range `s`, with its address masked, so `cidr("10.244.1.0/16") == cidr("10.244.0.0/16")`. |
| `<cidr>.ip(n)` | The address `n` places after the range's first, which must lie in the range: `cidr("10.96.0.0/12").ip(10)` is `10.96.0.10`. |
| `<cidr>.prefix()` | The prefix length, an int. |
| `<cidr>.contains(<ip>)` | Whether the range holds the address. |
| `ip(s)` | The address `s`, equal to every spelling of it. |
| `<ip>.family()` | `ipv4` or `ipv6`. |
| `string(<cidr>)`, `string(<ip>)` | The canonical spelling: `fd00::/108`, `fd00::a`. |
| `bytes(s)` | A size such as `1G` or `512M` in bytes, an int, with suffixes in powers of 1024. It replaces CEL's conversion of a string to bytes. |
| `isSize(s)` | Whether `bytes(s)` reads `s`: digits with an optional K, M, G, T or P suffix, below 8 EiB. |

## The build compiles every rule

The image build runs `chalkos-rules check` over the image's schemas, facts, effects and outputs.
It fails on a rule that does not parse or type-check, on a rule that reads a fact no module
declares, and on a command fact that is no program of the store. It also limits a rule's cost:

- The build refuses a rule whose estimated cost exceeds 10,000,000, estimated with 256 elements
  in every list, map and string. A loop over a kind's documents inside another loop over them
  fits; a third nested loop does not.
- A node stops a rule at a cost of 1,000,000 on the config it meets.

A rule of the running image that does not compile on the node fails the delivery as an internal
error.

## Errors show no values

A refusal must not leak a secret or a fact's content that the rule only compared:

- A rule that cannot be evaluated refuses the delivery with the kind of error alone, such as
  `cannot check: Peer "router-a" spec.asn: a rule cannot be evaluated: no such key`. The kind is
  one of `no such key`, `index out of bounds`, `no such overload`, `unknown time zone`,
  `invalid input to a function`, `cost limit exceeded`, `overflow`, `division by zero` and
  `conversion error`. An error of chalkos's functions names the function, such as
  `ip: the argument is not an address`. Of any other error the refusal says only
  `a rule cannot be evaluated`.
- A `messageExpression` that fails gives the same kind of error after the message.
- A rule that yields no bool says which type it yields.
- A command fact that fails shows its exit status alone: `cannot check: fact disks: the command
  failed (exit 1); see the node's journal`. Its standard error goes to the node's journal, cut to
  4 KiB. An output that is no JSON, and a file that is not JSON, show nothing of their content.

A message itself can show what a fact holds, as the `Rack` message shows the survey's location.
Keep what a rule does not need out of the fact with `fields`, or as hashes with `hashOthers`.

## A command fact runs in a sandbox

chalkd runs a command fact through `systemd-run --pipe --wait` as a transient unit, as root
without `CAP_SYS_MODULE`:

- The file system is read-only, STATE among it, apart from `/dev`, the unit's own empty `/tmp`
  and `/var/tmp`, and `/run/cryptsetup`, cryptsetup's lock directory, which the passphrase test
  writes.
- Home directories are hidden, and kernel tunables and control groups are read-only.
- The unit cannot load a kernel module itself. The kernel still loads one on request, as for a
  socket family or a cipher the unit uses.
- It has no network and no socket beyond Unix sockets and the kernel crypto API's.
- Devices stay accessible, because the storage facts resolve disks and test passphrases on block
  devices. cryptsetup may set up a temporary device-mapper mapping for a cipher the kernel crypto
  API lacks.
- It gains no privileges.

A command that exits with an error, takes longer than 30 seconds, prints more than 1 MiB or prints
no JSON refuses the delivery.

## Rules compare with the running config alone

- Rules compare the config with the running image's config. The one check that reads another
  slot is a storage change of the running image's config, which also evaluates the storage rules
  against the staged image's slot.
- A command fact runs with the running image's program, so a fact that a newer image adds is null
  until that image runs.

## Built into chalkos

chalkos's own kinds guard changes the same way:

| Kind | Rule | Takes |
| --- | --- | --- |
| `SystemDisk` | VAR keeps its format, encryption and size; a larger VAR, or one that fills the disk, is accepted. | reinstall |
| `Volume` | A volume keeps its disk, and a changed disk reference finds the disk it lives on. | reinstall |
| `Volume` | A volume keeps its format and encryption, also the encryption it inherits from the `SystemDisk`, and does not shrink or take a fixed size where it filled its disk. | reset |
| `Volume` | A disk reference that differs from the one its disk was pinned by resolves, and the pinned disk of a volume the config creates or changes is plugged in. | refuse |
| `Volume` | No volume the machine records is left out. | reinstall |
| `Volume` | A new encrypted volume comes with the fallback secret, which opens the node's fallback keyslot. | refuse |

They, and the `when` rules of the built-in effects, read these facts:

| Fact | Source | Value |
| --- | --- | --- |
| `recorded` | `/state/storage/storage.json` | The storage section STATE records. |
| `disks` | `chalkos-storage facts disks` | For each volume with a disk of its own, whether its disk is pinned and plugged in, and what a changed reference finds. |
| `fallback` | `chalkos-storage facts fallback` | Whether a fallback secret was sent, and whether it opens the node's fallback keyslot. |

The storage kinds' messages show what the `disks` fact found: a volume's disk reference, the
identity of its pinned disk and of the disk a changed reference finds, and why a disk cannot be
found.

A reset recreates its volume like a new one, so a node with a fallback needs the fallback secret.
`chalkos-storage facts fallback` checks it against the password keyslots of STATE and the volumes
before the reset. The volume being reset may hold the node's only password keyslot. A secret that
does not open that keyslot then refuses the reset, and so does a keyslot cryptsetup cannot read,
as when its header is damaged, although the reset would wipe it.

!!! note "Kubernetes: the Node and Cluster kinds keep the node's share"

    Images with Kubernetes add two rules. The `Node`'s type stays the one the node's Kubernetes
    share is for, read from `facts.share`. The `Cluster`'s `ipFamilies`, and the pod ranges,
    service ranges and DNS addresses of the families both configs have, stay once the node holds
    a share. Ranges and addresses compare as what they denote; an unset DNS address is the 10th
    of its service range. Both take `refuse`.

    Two facts come with them. `share` is `/state/kubernetes/share.json`: the kind of the node's
    share and the SHA-256 of each of its other keys, so its certificates and private keys stay
    out. `cluster` is `/run/chalkos/kubernetes/cluster.json`, the node's cluster as chalkd last
    wrote it.

## Check that it worked

Build the image, which compiles every rule. Then run `chalkctl validate`, which runs the rules of
kinds and the field rules that read neither `oldSelf` nor facts:

```sh
chalkctl validate
```

The rules that read `oldSelf` run on `chalkctl apply-config`, against the node's config. Rules
that read facts run on the node alone. A refusal that starts with `cannot check:` comes from a
rule that cannot be evaluated or a fact that cannot be loaded. The journal of `chalkd.service`
holds what a failed command fact wrote.

## What next

- [Customise the installer](customise-installer.md) for what the installer image carries.
- [React to changes](react-to-changes.md) for `when`, which uses the same CEL and facts.
- [The config pipeline](../how-it-works/node/config-pipeline.md) for where rules run in a
  delivery.
- [Config and the inventory](../how-it-works/node/config.md) for the changes a node accepts.
