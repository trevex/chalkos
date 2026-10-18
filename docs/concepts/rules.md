---
title: "Change rules and facts"
description: "How an image's CEL rules decide which config changes a node accepts and what they restart or reload, and the node state they read"
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
The units that read the config, chalkos's own among them, declare in rules of the same kind
which changes restart or reload them.

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
        rule = "facts.rackSurvey == null || self == facts.rackSurvey.location";
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
| `messageExpression` | CEL that yields the message from the rule's variables. When it fails, the node says `message`, or without one names the rule's kind or field. |
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

A rule without `message` says `the config does not meet a rule of Kubernetes spec.ipFamilies`,
naming the kind or the field that declares it rather than the rule itself, which can be long. One
whose `messageExpression` fails says so after that, with the kind of error it fails with, as
below.

A rule that cannot be evaluated refuses the delivery with an error that names the kind, or the
kind, the document and the field, and the kind of error, but no value the rule read:

```text
cannot check: Kubernetes "kubernetes" spec.ipFamilies: a rule cannot be evaluated: no such key
```

The kind of error is one of `no such key`, `index out of bounds`, `no such overload`,
`unknown time zone`, `invalid input to a function`, `cost limit exceeded`, `overflow`,
`division by zero` and `conversion error`, or the whole error of one of chalkos's functions, such
as `ip: the argument is not an address`, which holds no value. CEL's errors show the key, the index
or the argument they are about, which can be a value the rule read, so the node shows nothing else
of them, and of an error of any other kind says only `a rule cannot be evaluated`. A rule that
yields no bool says which type it yields instead.

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
so a rule can tell whether a secret changed without reading it. Maps iterate in the order of their
keys, so a list built from the names of `documents` is sorted.

## What is a fact?

A fact is node state a rule reads as `facts.<name>`, declared in
[`chalkos.facts`](../reference/options.md#chalkosfacts) and written to `/etc/chalkos/facts.json`.
It is one of:

- a file, `{ file = "/state/kubernetes/share.json"; fields = [ "kind" ]; }`, whose JSON is the
  fact, and null while the file is missing. `fields`, when given, keeps only those keys of the
  file's JSON object, so a rule sees nothing else of the file. `hashOthers = true` keeps the
  object's other keys too, each as the hex SHA-256 of its value, so a rule sees whether a value
  changed but not what it holds. A file that cannot be read, holds no JSON or, with `fields` or
  `hashOthers`, no JSON object refuses the delivery;
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
| `ProtectSystem=strict`, `ReadOnlyPaths=-/state` | The file system, STATE among it, is read-only except `/dev`, the unit's own `/tmp` and `/var/tmp`, and `/run/cryptsetup`, cryptsetup's lock directory, which stays writable for the passphrase test. |
| `ProtectHome=yes`, `PrivateTmp=yes` | Home directories are hidden, and `/tmp` and `/var/tmp` are the unit's own, empty and writable. |
| `ProtectKernelTunables=yes`, `ProtectKernelModules=yes`, `ProtectControlGroups=yes` | Kernel tunables and control groups are read-only. The unit cannot load a kernel module itself, since `ProtectKernelModules` drops `CAP_SYS_MODULE`, but the kernel still loads one on request, as for a socket family or a cipher the unit uses. |
| `PrivateNetwork=yes`, `RestrictAddressFamilies=AF_UNIX AF_ALG` | No network, and no socket but Unix sockets and the kernel crypto API's. |
| `NoNewPrivileges=yes` | No program gains privileges it does not have. |

The unit runs as root with root's capabilities but `CAP_SYS_MODULE`, and devices stay accessible:
resolving disks and testing a passphrase read block devices, and cryptsetup may set up a temporary
device-mapper mapping for a cipher the kernel crypto API lacks. A command that exits with an error,
takes longer than 30 seconds or prints more than 1 MiB, or whose output is no JSON, refuses the
delivery:

```text
cannot check: fact disks: /nix/store/...-chalkos-storage-facts did not finish within 30s
```

A command can echo the config it reads, so the error of one that fails shows only its exit status,
and what it wrote to standard error goes to the node's journal, cut to 4 KiB:

```text
cannot check: fact disks: the command failed (exit 1); see the node's journal
```

The error of a command whose output is no JSON, and of a file that is not JSON, shows nothing of
the output or of the file's content either.

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

chalkd compiles the rules of every image with its own CEL library, the running chalkd's: an
upgrade or `apply-config --staged` compiles the new image's rules before that image's chalkd runs,
so a slot for an image whose rules call a function the running chalkd lacks is refused. Rules of
the running image that do not compile fail the delivery as an internal error of the node.

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

## Which units does a change restart or reload?

A unit under [`chalkos.consumers`](../reference/options.md#chalkosconsumers), one that reads
config kinds or that a delivery acts on, has a restart rule and a reload rule: CEL expressions
that are true when a delivery restarts or reloads the unit. bird reads its peers again on
`birdc configure`, which keeps the sessions a restart would drop:

```nix
chalkos.consumers.bird = {
  kinds = [ "Peer" ];
  restart.when = "false";
  reload = {
    when = ''"Peer" in changedKinds'';
    command = "${pkgs.bird2}/bin/birdc configure";
  };
};
```

| Option | Default | Meaning |
| --- | --- | --- |
| [`restart.when`](../reference/options.md#chalkosconsumersrestartwhen) | `"<kind>" in changedKinds \|\| …` for each of the unit's kinds; `false` without kinds | When a delivery restarts the unit. |
| [`restart.start`](../reference/options.md#chalkosconsumersrestartstart) | `false` | Whether a restart starts the unit too when it does not run. |
| [`reload.when`](../reference/options.md#chalkosconsumersreloadwhen) | `false` | When a delivery reloads the unit. |
| [`reload.command`](../reference/options.md#chalkosconsumersreloadcommand) | `systemctl reload <unit>` | The command that reloads it. |

They see `config` and `oldConfig`, the config the node runs once the delivery is applied and the
running slot's config before it, in the shape change rules see them, and these:

| Variable | Value |
| --- | --- |
| `changedKinds` | The names of the kinds whose documents differ between the two configs, or a `Secret` that one of their `secretRef` fields names. |
| `facts.<name>` | The fact once the delivery is applied. |
| `oldFacts.<name>` | The fact before the delivery changed anything. |

`changedKinds` tells what `config` and `oldConfig` alone do not: which `Secret`s a kind's
documents reference is in the kind's schema, and a unit reads their values as its credentials.
chalkd loads the facts the rules read before a delivery changes anything, and again once it is
applied, so a rule compares a file that the delivery writes, such as the node's Kubernetes share.

chalkd evaluates every consumer's rules once it has applied a delivery, after it stored the config
in the running slot, also for a delivery without config, as of a share:

1. It reloads each unit whose reload rule is true and that runs, with its command. A unit that
   does not run reads its config when it starts, and a unit whose restart rule is true too is
   restarted instead.
2. It restarts each unit whose restart rule is true: in one `systemctl try-restart` the units that
   run, or ran and stay active, so a unit that never started or was stopped stays so; in one
   `systemctl restart` those with `restart.start` set; and with `systemctl reset-failed` and
   `restart` those that failed, since the new config may be what they lacked.

A rule that cannot be evaluated counts as true: a unit restarts once more than it needed to rather
than run on config it has not read. Its error, which shows none of the values the rule read, goes
to the node's journal and back to the client as a warning.
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) prints the restarted units and
the warnings. A reload or restart that fails fails the delivery, and the error names the units to
reload or restart by hand.

Until a delivery is applied whole, the rules compare with the state from before the first delivery
that failed part way, not with what that delivery wrote: chalkd records that state's config and
the facts its rules read in `/run/chalkd/effects-baseline.json`, readable by root alone, with
`Secret` values as their hashes and facts as their definitions keep them. A retry, or another
delivery, takes every effect the failed one did not, and the delivery that succeeds removes the
file. A reboot clears it, since every unit starts at boot.

A consumer named `chalkd` is chalkd itself, which acts on its rules in-process: a reload starts
the control plane's loop again, which reads the share and applies the control plane's objects, and
a restart every Kubernetes loop, which read the cluster when they start. It acts on them also
when a delivery fails to store the slot or to write the consumers' config, since the cluster file
and the share its loops read are written by then. The built-in consumers are these:

| Unit | Rules |
| --- | --- |
| `systemd-networkd` | Reloads with `networkctl reload` when a `Network`, `NetDev` or `Link` document changes, or a `Secret` one of them references. |
| `chronyd` | Reloads with `chronyc reload sources` when the `TimeSync`'s servers change. |
| `chalkos-chrony-dhcp`, `chalkos-controlplane`, `chalkos-registry-mirrors` | Restart when a document of their kinds changes: `TimeSync`, `Node` and `RegistryMirror`. |
| `chalkos-kubernetes`, `kubelet` | Restart, and start when they do not run, when the `Node`'s name, host name or type, the `Kubelet`, the `Address=` values of the `Network` documents' `[Network]` sections, the `Cluster`, `Kubernetes` or `Flannel` document, the cluster file or the share changes, but not for a share that differs in its node CA alone. On images with Kubernetes. |
| `chalkd` | Restarts its Kubernetes loops when the cluster changes, and reloads the control plane's loop when the share changes beyond its node CA or, on a control plane, the objects of the `Addon` documents. On images with Kubernetes. |

## How is a rule checked when the image is built?

The image build runs `chalkos-rules check` over the image's schemas, facts and consumers, and
fails on any rule that does not parse or type-check, a consumer's rules among them, on a rule
that reads a fact the image does not declare and on a command fact that is no program of the
store. It also refuses a rule whose estimated cost
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
| `Volume` | A disk reference that differs from the one its disk was pinned by resolves, and the pinned disk of a volume the config creates or changes is plugged in. | refuse |
| `Volume` | No volume the machine records is left out. | reinstall |
| `Volume` | A new encrypted volume comes with the fallback secret, which opens the node's fallback keyslot. | refuse |

They, and the rules of the built-in consumers, read these facts:

| Fact | Source | Value |
| --- | --- | --- |
| `share` | `/state/kubernetes/share.json` | The kind of the node's Kubernetes share and the SHA-256 of each of its other keys, so its certificates and private keys stay out; declared on images with Kubernetes. |
| `recorded` | `/state/storage/storage.json` | The storage section STATE records. |
| `disks` | `chalkos-storage facts disks` | For each volume with a disk of its own, whether its disk is pinned and plugged in, and what a changed reference finds. |
| `fallback` | `chalkos-storage facts fallback` | Whether a fallback secret was sent, and whether it opens the node's fallback keyslot. |
| `cluster` | `/run/chalkos/kubernetes/cluster.json` | The node's Kubernetes cluster as chalkd last wrote it, which the kubelet's rules compare before and after a delivery; declared on images with Kubernetes. |

## Limits

- A rule binds `self` to a path through the properties of objects, so it cannot be declared on the
  items of a list or the values of a map; it is declared on the list or the map, which a kind's
  rule or a field's rule compares whole. Lists are not matched item by item by a key.
- A consumer's rule compares documents as written, so a change that makes no difference to the
  unit still counts: an `Address=` value whose prefix length alone changes restarts the kubelet,
  and adding a `TimeSync` that names chalkos's own servers reloads chrony's sources.
- Rules compare the config with the running image's config alone. The one check that reads another
  slot's config is that of a storage change of the running image's config, which evaluates the
  storage rules against the staged image's slot as it would be written again.
- A command fact runs on the node that evaluates the rules, with the running image's program, so a
  fact that a newer image adds is null until that image runs.
- A message can show a fact's content in an error the node returns. A file fact's `fields` keep
  what the rules do not need of the file out of the fact, and `hashOthers` keeps the rest as
  hashes, as of the share's private keys. The error of a rule that cannot be evaluated or a
  `messageExpression` that fails shows no value, only the kind of error.
- The storage kinds' messages show what the `disks` fact found: a volume's disk reference, the
  identity of its pinned disk and of the disk a changed reference finds, and why a disk cannot be
  found.
- A reset recreates its volume like a new one, so a node with a fallback needs the fallback
  secret, which `chalkos-storage facts fallback` checks against the password keyslots STATE and
  the volumes have before the reset. Where the volume being reset holds the node's only password
  keyslot, a secret that does not open it, or a keyslot cryptsetup cannot read, as when its header
  is damaged, refuses the reset, although the reset would wipe that keyslot.

## Related pages

- [Config and the inventory](config.md) for the changes a node accepts and the slots rules compare.
- [Storage and encryption](storage.md) for what the storage rules protect.
- [Customise the image](../guides/customise-image.md#add-a-rule-and-a-fact-to-a-kind) for a rule and
  a fact on an extension's kind.
- [`chalkos.kinds`](../reference/options.md#chalkoskinds),
  [`chalkos.facts`](../reference/options.md#chalkosfacts) and
  [`chalkos.consumers`](../reference/options.md#chalkosconsumers) for the options.
