---
title: "Change a node's config"
description: "Deliver changed YAML to a running node with chalkctl apply-config, preview it with --diff and act on a refusal"
---

# Change a node's config

This page is for an operator who changes the YAML files of nodes that are installed and running.
At the end each node runs the new config, and the services that read what changed have restarted
or reloaded. A config change needs no new image and no reboot. The page gives the homelab's nodes
their own time servers, as [Write a node's config](node-config.md#timesync-names-the-time-servers)
describes.

## Before you begin

You need the workstation that [Set up your workstation](workstation.md) describes, with the secrets
file `secrets.age`. `chalkctl apply-config` needs the secrets file. No client file suffices.

The node runs, and chalkctl reaches it at the address the inventory names.

## Change the files and validate them

Edit the files the inventory gives the node. This file goes to every node, because the homelab's
inventory gives each node the files of `cluster/`:

```yaml title="cluster/time.yaml"
apiVersion: time.chalkos.dev/v1alpha1
kind: TimeSync
metadata: {name: default}
spec:
  servers:
    - host: ntp1.example.internal
    - host: ntp2.example.internal
      nts: false
```

Run `chalkctl validate`. It checks each node's files against the schemas of the node's image in
the inventory, offline:

```console
$ chalkctl validate
cp1: valid for the image node-metal (node on metal, root hash d871203debfe), config 0f55ac1ecfcd83116bbd9b30b5cf2d37c94db754281d1debe29c5d31546d0e90
w1: valid for the image node-metal (node on metal, root hash d871203debfe), config ec7ca85e562721ea1f8d0368d415bb80dc62819ba0c80857c9c444e4450aba09
```

Each node's config hash differs from the one before the change. That hash is the config's version,
which `apply-config` and `chalkctl status` print too.

## Preview the change

`--diff` asks the node for the config it runs, compares it with the inventory's and changes
nothing:

```console
$ chalkctl apply-config w1 --secrets secrets.age --diff
w1: config 1 added, 0 removed, 0 changed
+ TimeSync "default"
```

The first line counts the documents the delivery adds, removes and changes. Each further line names
one document by its kind and name:

| Line | Meaning |
| --- | --- |
| `+ TimeSync "default"` | The inventory adds the document. |
| `- Volume "old" (on the node, not in the inventory)` | The delivery removes the document. |
| `~ Node "default"` | The document changes. One indented line follows for each field that changed. |
| `spec.labels.room: (none) → "b"` | A field's path, its old value and its new one, as JSON. `(none)` marks a field that is not set. |
| `spec.data.token: changed` | A field of a `Secret` changed. Secret values are compared by their hashes and never shown. |

The comparison takes the documents as written, without defaults. A field that one side spells out
and the other leaves to its default shows as changed.

## Deliver the config

Run the command without `--diff`:

```console
$ chalkctl apply-config w1 --secrets secrets.age
w1: config 1 added, 0 removed, 0 changed
restarted <unit>
w1 runs config ec7ca85e562721ea1f8d0368d415bb80dc62819ba0c80857c9c444e4450aba09
```

`restarted <unit>` stands for one line per unit that the delivery restarted. Which units appear
depends on the image's effects and on which units run, so the lines differ between nodes. A change
to storage adds a line per volume, such as `volume scratch: new volume`.

chalkctl first asks the node which image it runs, and checks the files against that image's
schemas. It also runs the image's change rules that read no node facts, against the config the
node runs. A file that fails either check stops the command before anything is sent.

The node replaces its config as a whole. It checks the documents again, evaluates every rule with
its facts, renders the outputs of the image and checks them. Any failure refuses the whole
delivery, and the node keeps the config it ran. Then it applies additive storage changes, writes
the config and takes the effects.

Deliver the config to each node whose files changed, one command per node. The homelab's change
reaches `cp1` as well:

```sh
chalkctl apply-config cp1 --secrets secrets.age
```

## Only the services that read a change restart

An effect is what a delivery does to the node's services once it is applied: reload a unit,
restart it or run a command. The image declares its effects, and each one names the outputs, kinds
or condition that fire it. A delivery that changes nothing an effect watches takes no effect, so
delivering the same config twice restarts nothing the second time.

chalkos's own effects reload networkd when the network changes and chrony when the time servers
do. [Built-ins](../reference/builtins.md#effects) lists them, and those of images with Kubernetes.

chalkctl lists restarted units alone. A reload or a command prints no line.
[React to changes](../build/react-to-changes.md) describes how image authors declare effects and
the order chalkd takes them in.

A reload, command or restart that fails fails the delivery. The config is applied by then, so the
error ends with what is still to do, such as `reload bird.service by hand once that is fixed, or
deliver it again`.

## A refusal names what the change takes

Change rules guard fields that a node cannot change in place, such as a volume's format. A config
that breaks a rule is refused as a whole, and nothing of it is applied. The refusal starts with the
most that any broken rule needs:

| The refusal starts with | What to do |
| --- | --- |
| `the config is refused:` | Change the files back, or make the change the message suggests instead. |
| `the config takes chalkctl storage reset:` | Reset the volume the message names, which wipes its data, then deliver the config again. |
| `the config takes a reinstall of <node>:` | Keep the old value, or reinstall the node with the new config. |

The messages of every broken rule follow, separated by `;`. The storage rules read node facts, so
the node refuses a storage change itself. chalkctl prints the node's error after `chalkctl:` and
the error's code. Its message part looks like this:

```text
the config takes chalkctl storage reset: Volume scratch: format xfs instead of ext4; reset it with chalkctl storage reset w1 scratch
```

A rule that reads no facts fails in chalkctl before anything is sent. That error names the node
first, as in `chalkctl: w1: the config is refused: ...`.

A volume that changes its disk, VAR that changes its format and a volume that the files no longer
name all take a reinstall. [Add storage volumes](storage-volumes.md) lists which storage changes
a running node makes, and [Reset a volume](storage-volumes.md#reset-a-volume) wipes one.
[Reinstall a node on its machine](add-remove-nodes.md#reinstall-a-node-on-its-machine) covers the
reinstall.

An image's own kinds bring their own rules and messages. `--diff` runs no rules, and `validate`
runs none that reads node facts, so neither shows those refusals.
[Guard changes with rules and facts](../build/guard-changes.md) explains how image authors write
them.

## Deliver config to a staged image

Each image on a node keeps its own config. After `chalkctl upgrade --no-reboot`, the image staged
to boot next has its own config until the node reboots into it. `--staged` delivers to that image:

```sh
chalkctl apply-config w1 --staged --secrets secrets.age
```

chalkctl checks the files against the node's image in the inventory, and the node checks them
against the staged image. Nothing restarts. The command ends with
`w1 boots config <version> with its staged image`.

Without `--staged`, the command changes the running image's config alone and warns:
`w1 has a staged image with its own config (version <version>); apply-config --staged updates it`.

A change to the running image that adds a volume the staged image's config lacks is refused.
Once the volume exists, the staged image's config would count as removing it. Deliver the change
with `--staged` first, which creates the volume, then without.

## Check that it worked

`chalkctl status` shows the config the node runs and whether it is the inventory's:

```console
$ chalkctl status w1 --secrets secrets.age
config ec7ca85e562721ea1f8d0368d415bb80dc62819ba0c80857c9c444e4450aba09 (the inventory's)
...
```

A node whose files changed since the last delivery shows
`not the inventory's; chalkctl apply-config w1 delivers it` in the parentheses.

## Roll back a config change

A node keeps the config its running image ran before the last delivery, its previous config.
`--rollback` delivers that config again in place of the inventory's. Preview it with `--diff`
first, which prints what the rollback changes and changes nothing:

```console
$ chalkctl apply-config w1 --secrets secrets.age --rollback --diff
w1: config 0 added, 1 removed (TimeSync "default"), 0 changed
- TimeSync "default" (in the running config, not in the previous one)
```

Then run it without `--diff`:

```console
$ chalkctl apply-config w1 --secrets secrets.age --rollback
w1: config 0 added, 1 removed (TimeSync "default"), 0 changed
restarted <unit>
w1 runs config <previous-version> again; config ec7ca85e562721ea1f8d0368d415bb80dc62819ba0c80857c9c444e4450aba09 is its previous one now
```

The node keeps the previous config itself, so the command needs no inventory. Without one, give
the node's address with `--address`. The command needs the secrets file, as any delivery does.

The previous config goes through the same checks, storage changes and restarts as any delivery.
The image's rules may refuse it. A rollback of a delivery that added a volume, for example, removes
the volume, which takes a reinstall, so the node refuses it with the storage rule's message:
`Volume scratch: the config removes it; only a reinstall of w1 removes a volume`.

The node keeps one previous config per image. The config a rollback replaces becomes the previous
one, so a second `--rollback` undoes the first. An install or an upgrade writes a config without a
previous one, so an upgraded node has none until its first delivery.

A rollback restores the config documents and nothing else. The OS CAs, the node certificate and
the payloads of the node's layers, such as a Kubernetes share, stay as they are. `--rollback`
refuses `--staged`, `--force` and the layers' payload flags such as `--kubernetes-share`.

`chalkctl status` names the config a rollback restores, below the `config` line:

```text
previous config <previous-version> (apply-config --rollback restores it)
```

A node without a previous config refuses the rollback with
`w1 keeps no previous config of the image it runs; it keeps one once apply-config replaced the
config the image was installed or upgraded with`.

## If something goes wrong

- `pass the secrets file with --secrets FILE, or --secrets - to read it from standard input`: the
  command ran with a client file. Pass the secrets file.
- `chalkctl: warning: w1: ...`: chalkd could not evaluate the condition of an effect. It took the
  effect all the same, so a unit restarted that might not have needed to.
- `no image is staged to boot next; apply the config to the running image`: `--staged` found no
  staged image. Run the command without it.
- `--staged delivers config alone; deliver the Kubernetes share to the running image`: `--staged`
  and `--kubernetes-share` do not combine.
- A unit that the change should have restarted did not restart. A restart effect leaves a stopped
  unit stopped, unless the effect starts it too.
  [Troubleshooting](troubleshooting.md) helps with a unit that fails after the delivery.

## What next

- [Upgrade nodes](upgrade.md) installs a new image, which a change to the image's modules needs.
- [Add storage volumes](storage-volumes.md) plans volumes and the changes a running node accepts.
- [The config pipeline](../how-it-works/node/config-pipeline.md) explains how a node checks,
  applies and keeps a delivery.
