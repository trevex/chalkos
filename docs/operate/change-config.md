---
title: "Change a node's config"
description: "Deliver changed YAML to a running node with chalkctl config apply, preview it with --diff and --dry-run, try it with --try and act on a refusal"
---

# Change a node's config

This page is for an operator who changes the YAML files of nodes that are installed and running.
At the end each node runs the new config, and the services that read what changed have restarted
or reloaded. A config change needs no new image and no reboot. The page gives the three nodes of
the `cluster-metal` template their own time servers, as
[Write a node's config](node-config.md#name-the-time-servers) describes.

In short:

```sh
chalkctl validate
chalkctl config apply cp1 --secrets=secrets.age --diff
chalkctl config apply cp1 --secrets=secrets.age --dry-run
chalkctl config apply cp1 cp2 cp3 --secrets=secrets.age
chalkctl status cp1
```

## Before you begin

You need the workstation that [Set up your workstation](workstation.md) describes, with the secrets
file `secrets.age`. `chalkctl config apply` needs the secrets file. No client file suffices.
`chalkctl status` runs with your client file.

The commands of this page are under [`chalkctl config`](../reference/cli/chalkctl_config.md).
[`chalkctl apply`](../reference/cli/chalkctl_apply.md) is the short form of `chalkctl config apply`
and takes the same flags.

The node runs, and chalkctl reaches it at the address the inventory names.

## Change the files and validate them

Edit the files the inventory gives the node. This file goes to all three nodes, because
the template's `spec.all` gives every node the files of `cluster/`:

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
cp1: valid for the image node-metal (node-metal on metal, root hash 98bb85096ed9), config 415c8f6a2ad1aaf92090cbf7b9814c5384220d2ec63d276334234e2bfcef86ad
cp2: valid for the image node-metal (node-metal on metal, root hash 98bb85096ed9), config 9c169c865266d9458e85251f6b142b4f47f1b836e36cbee81f0c8f91d3510676
cp3: valid for the image node-metal (node-metal on metal, root hash 98bb85096ed9), config ee7691337f6f5c53b258096e80be25dc05fe939c91920f55286d5d8177aa295c
```

The config hash of each node differs from the one before the change. That hash is the config's
version, which `config apply` and `chalkctl status` print too.

`validate` decrypts no `sops://` or `age://` source of a `Secret` document, so it asks for no
key. It checks the config with a placeholder for each such value, warns of each one, and prints
`config version unknown` in place of the hash, which covers the values. Whether a source decrypts
and holds its value shows when `--diff`, `--dry-run` or a delivery reads it.

## Preview the change

`--diff` asks the node for the config it runs, compares it with the inventory's and changes
nothing:

```console
$ chalkctl config apply cp1 --secrets=secrets.age --diff
cp1: config 1 added, 0 removed, 0 changed
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

## Check the change on the node

`--dry-run` has the node run every check of a delivery and changes nothing. Besides the schemas,
it runs the change rules with their facts, and each output's renderer and check. `chalkctl
validate` and `--diff` run no renderer, so a config they accept can still be refused on delivery.
A dry run shows that refusal first:

```console
$ chalkctl config apply cp1 --secrets=secrets.age --dry-run
cp1: config 1 added, 0 removed, 0 changed
+ TimeSync "default"
cp1: config <previous-version> → <version>
  outputs:  chrony
  effects:  chrony: run chalkd chrony-reload-sources (output chrony)
            chrony-dhcp: restart chalkos-chrony-dhcp.service (rule)
  storage:  none
```

The diff comes first, as `--diff` prints it. The plan follows:

| Line | Meaning |
| --- | --- |
| `config <previous-version> → <version>` | The config the node runs and the one it would run after the delivery. `none` stands for a node without config. |
| `outputs:` | The outputs whose rendered files would change, or whose documents reference a `Secret` whose value changes. |
| `effects:` | One line per effect the delivery would take: its actions, in the order the node takes them, and in parentheses what fires it, `output <name>`, `kind <name>` or `rule`. |
| `storage:` | The storage changes the delivery would make, one line per volume, as a delivery prints them. |

A refusal prints the error a delivery would print, and the command exits 1. The node's config,
outputs, storage and status stay as they were, and no unit restarts.

`--dry-run` also takes several nodes, such as `cp1 cp2`. A node that refuses its config, or
that the dry run cannot check, does not stop the others. Each error names its node, and the last
line counts both kinds, such as
`the config of 1 node is refused: cp1; the dry run failed on 1 node: cp2`.

## Deliver the config

Run the command without `--diff`:

```console
$ chalkctl config apply cp1 --secrets=secrets.age
cp1: config 1 added, 0 removed, 0 changed
restarted <unit>
cp1 runs config 415c8f6a2ad1aaf92090cbf7b9814c5384220d2ec63d276334234e2bfcef86ad
```

`restarted <unit>` stands for one line per unit that the delivery restarted. A reload or a command
prints no line. Which units appear depends on the image's effects and on which units run, so the
lines differ between nodes. chalkos's own effects reload networkd when the network changes and
chrony when the time servers do; [Built-ins](../reference/builtins.md#effects) lists them. A
delivery that changes nothing an effect watches restarts nothing, so the same config delivered
twice restarts nothing the second time. A change to storage adds a line per volume, such as
`volume scratch: new volume`.

chalkctl and the node both check the config before the node changes anything. Any failure refuses
the whole delivery, and the node keeps the config it ran.
[The config pipeline](../how-it-works/node/config-pipeline.md) follows a delivery through the node.

Deliver the config to each node whose files changed. One command takes several nodes and delivers
to them one after the other. The change reaches `cp2` and `cp3` as well:

```sh
chalkctl config apply cp1 cp2 cp3 --secrets=secrets.age
```

chalkctl checks every node before the first one gets anything, and stops at the first node that
fails. A node that runs the config already gets nothing, and chalkctl prints
`cp2 runs config <version> already`. Run the same command again to continue a run that stopped.

A config that changes a version the cluster bounds, such as the Kubernetes API server's, goes to
the control planes first, one at a time and on trial, also when the command names one node.
[Roll the control plane out through config](../kubernetes/upgrade-kubernetes.md#roll-the-control-plane-out-through-config)
describes that run.

## Deliver config to a staged image

After `chalkctl upgrade --until stage` or `--until activate`, the node's inactive slot holds an
image that boots with config of its own. A plain `config apply` delivers to that image too, once
both images have checked the config:

```console
$ chalkctl config apply cp1 --secrets=secrets.age
cp1: config 1 added, 0 removed, 0 changed
cp1 runs config <version>
cp1 boots config <version> with its staged image too
```

`--staged` delivers to the staged image alone, staged or active, and restarts nothing:

```sh
chalkctl config apply cp1 --staged --secrets=secrets.age
```

The command ends with `cp1 boots config <version> with its staged image`. With `--dry-run`, the
staged image checks the config and the node keeps nothing.

On a node without a staged image, `--staged` holds the config for the next upgrade, as the node's
pending config:

```console
$ chalkctl config apply cp1 --staged --secrets=secrets.age
cp1: config 1 added, 0 removed, 0 changed
cp1 holds config <version> pending, unchecked; the next upgrade checks it against its image
```

A later `config apply` without `--staged` replaces the pending config with the config it delivers.
`chalkctl config discard` removes the pending config and needs no inventory:

```console
$ chalkctl config discard cp1 --secrets=secrets.age
cp1 dropped its pending config <version>
```

[A staged image and the next upgrade take config of their own](../how-it-works/node/config.md#a-staged-image-and-the-next-upgrade-take-config-of-their-own)
explains which image checks what and when each slot is written.

## Check that it worked

`chalkctl status` shows the config the node runs and whether it is the inventory's:

```console
$ chalkctl status cp1
cp1: healthy
config 415c8f6a2ad1aaf92090cbf7b9814c5384220d2ec63d276334234e2bfcef86ad (the inventory's)
...
```

A node that runs the inventory's image and whose files changed since the last delivery shows
`not the inventory's; chalkctl config apply cp1 delivers it` in the parentheses.

## Roll back a config change

A node keeps the config its running image ran before the last delivery, its previous config.
`chalkctl config rollback` delivers that config again in place of the inventory's. Preview
it with `--diff` first, which prints what the rollback changes and changes nothing:

```console
$ chalkctl config rollback cp1 --secrets=secrets.age --diff
cp1: config 0 added, 1 removed (TimeSync "default"), 0 changed
- TimeSync "default" (in the running config, not in the previous one)
```

Then run it without `--diff`:

```console
$ chalkctl config rollback cp1 --secrets=secrets.age
cp1: config 0 added, 1 removed (TimeSync "default"), 0 changed
restarted <unit>
cp1 runs config <previous-version> again; config 415c8f6a2ad1aaf92090cbf7b9814c5384220d2ec63d276334234e2bfcef86ad is its previous one now
```

The command needs the secrets file but no inventory: without one, give the node's address with
`--address`. The previous config goes through the same checks, storage changes and restarts as any
delivery. The config a rollback replaces becomes the previous one, so a second rollback undoes the
first. A rollback restores the config documents alone. The OS CAs, the node certificate and the payloads of
the node's layers, such as a Kubernetes share, stay as they are.

`chalkctl status` names the config a rollback restores, below the `config` line:

```text
previous config <previous-version> (config rollback restores it)
```

During a trial, `chalkctl config rollback` delivers the config from before the trial and ends
the trial, as [End a trial early](#end-a-trial-early) describes.

## Try a config change

A change can cut chalkctl off from the node, such as a change to the firewall or to the address
chalkctl reaches. `--try` applies such a change on trial. The node rolls the config back by itself
at the end of the duration unless chalkctl confirms it first. `--try` takes a duration from 10s to
24h and has no default:

```console
$ chalkctl config apply cp1 --secrets=secrets.age --try 5m
cp1: config 1 added, 0 removed, 0 changed
cp1: delivering config <version> on trial until <deadline>
restarted <unit>
cp1 runs config <version> on trial; it rolls back to config <previous-version> by itself unless confirmed within 5m0s
cp1: waiting for its layers to serve with the config
cp1: waiting for a new connection and the node's health to confirm config <version>
cp1 keeps config <version>: the trial is confirmed
```

chalkctl confirms the config over a new connection, once the image's layers find the node serving
with it and the node is healthy. A trial delivers config documents alone, and the node takes no
other change while it is open.
[A trial rolls a config back unless it is confirmed](../how-it-works/node/config.md#a-trial-rolls-a-config-back-unless-it-is-confirmed)
explains the checks and what a trial holds back.

With several nodes, `--try` tries the config on each in turn, and confirms one node's trial before
the next node gets the config. `--until deliver` delivers on trial to the first node that does not
run the config yet, leaves its trial open and stops, as
[Confirm a trial later](#confirm-a-trial-later) describes. `--no-confirm` takes one node.

### Confirm a trial later

`--no-confirm` leaves the trial open, for example to test the change by hand. The command ends
with the two commands that end the trial:

```text
chalkctl config confirm cp1 keeps it; chalkctl config rollback cp1 rolls it back now
```

`chalkctl config confirm` confirms the trial in the same way as `--try`, and tries until the
trial's deadline:

```sh
chalkctl config apply cp1 --secrets=secrets.age --try 30m --no-confirm
chalkctl config confirm cp1 --secrets=secrets.age
```

The node keeps the config on trial, so the command needs no inventory.

### End a trial early

`chalkctl config rollback` during a trial delivers the config from before the trial at once
and ends the trial. Its output ends with `cp1: the trial of config <version> ended`.

A reboot ends an open trial as well. The boot restores the config from before the trial before
networkd, the firewall and every other service start. Resetting the node therefore undoes a config
that cut it off. To try the config again after its trial rolled back, deliver it with `--try`.

## Override a failed runtime readiness check

If a `waitFor` check blocks a config delivery, fix the condition it reports and retry. If
you have verified that the dependent service can safely run, repeat the config delivery with
`--ignore-readiness`:

```sh
chalkctl config apply cp1 --secrets=secrets.age --ignore-readiness
```

The probe still runs within its configured deadline. A failure is reported as a warning and
the dependent effect can continue. Config validation, prerequisite actions, service failures
and the health checks for confirming a trial still apply. Use `--try` with
`--ignore-readiness` when the node can accept a trial and you want rollback if it becomes
unreachable. A later rollback runs its own readiness checks normally.

`--ignore-readiness` goes with config alone. A delivery with `--kubernetes-share` runs the
readiness checks as usual, and its own `--ignore-revoked` delivers the share without asking whether the
node is revoked.

## If it fails

A config that breaks one of the image's change rules is refused as a whole, and nothing of it is
applied. The refusal starts with the most that any broken rule needs:

| The refusal starts with | What to do |
| --- | --- |
| `the config is refused:` | Change the files back, or make the change the message suggests instead. |
| `the config takes chalkctl storage reset:` | Reset the volume the message names, which wipes its data, then deliver the config again. |
| `the config takes a reinstall of <node>:` | Keep the old value, or reinstall the node with the new config. |

The messages of every broken rule follow, separated by `;`. A rule that reads no facts fails in
chalkctl before anything is sent, and its error names the node first, as in
`chalkctl: cp1: the config is refused: ...`. The storage rules read node facts, so the node
refuses a storage change itself. chalkctl then prints the node's error after `chalkctl:` and the
error's code. Its message part looks like this:

```text
the config takes chalkctl storage reset: Volume scratch: format xfs instead of ext4; reset it with chalkctl storage reset cp1 scratch
```

[Add storage volumes](storage-volumes.md) lists which storage changes a running node makes, and
[Reset a volume](storage-volumes.md#reset-a-volume) wipes one.
[Reinstall a node on its machine](add-remove-nodes.md#reinstall-a-node-on-its-machine) covers the
reinstall. An image's own kinds bring their own rules and messages. `--diff` runs no rules, and
`chalkctl validate` runs only those that read no node facts, so a refusal by a rule that reads
node facts shows before a delivery only with `--dry-run`.
[Guard changes with rules and facts](../build/guard-changes.md) explains how image authors write
them.

Other common failures:

- `pass the secrets file with --secrets FILE, or --secrets - to read it from standard input`: the
  command ran with a client file. Pass the secrets file.
- `chalkctl: warning: cp1: ...`: chalkd could not evaluate the condition of an effect. It took the
  effect all the same, so a unit restarted that might not have needed to.
- An error that ends with what is still to do, such as `reload bird.service by hand once that is
  fixed, or deliver it again`: a reload, command or restart failed after the config was applied.
  Fix the unit, then do what the error says.
- A unit that the change should have restarted did not restart. A restart effect leaves a stopped
  unit stopped, unless the effect starts it too.
  [Troubleshooting](troubleshooting.md) helps with a unit that fails after the delivery.
- `cp1 runs config <version> on trial until <deadline>; confirm it with chalkctl config confirm cp1, or roll it back with chalkctl config rollback cp1, first`:
  a trial is open on the node, and it holds back other changes until it ends. Confirm the trial
  or roll it back first. `config apply --diff` still works.
- `--staged delivers config alone; deliver the Kubernetes share to the running image`: `--staged`
  and `--kubernetes-share` do not combine.

[Troubleshooting](troubleshooting.md#a-config-rollback-or-trial-is-refused) covers a refused rollback
or trial, a trial that rolled back, a staged image that kept its earlier config and a pending
config that an upgrade refuses.

A trial whose rollback at the deadline fails, or whose record the node cannot read, shows on the
trial's line of `chalkctl status` with the command that ends it, as
[Read the config line](status-and-logs.md#read-the-config-line)
lists.

## What next

- [Upgrade nodes](upgrade.md) installs a new image, which a change to the image's modules needs.
- [Add storage volumes](storage-volumes.md) plans volumes and the changes a running node accepts.
- [The config pipeline](../how-it-works/node/config-pipeline.md) explains how a node checks,
  applies and keeps a delivery.
