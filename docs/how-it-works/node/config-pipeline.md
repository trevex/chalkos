---
title: "The config pipeline"
description: "How a delivery passes a node's documents through rules, outputs and effects, at boot and after an upgrade"
---

# The config pipeline

A delivery of config to a node either changes nothing or changes everything it carries. When the
`Peer` documents of the homelab's BGP extension give bird a config that its own parser rejects,
`chalkctl apply-config` fails, and its error names the output:

```text
output bird-conf: its check refused the rendered files (exit 1); see the node's journal
```

The node then still runs its old documents, its old `bird.conf` and an unreloaded bird. This page
explains the stages that make that so. The build pages show how an image declares each stage:
[rules](../../build/guard-changes.md), [validators](../../build/validate-config.md),
[outputs](../../build/render-config.md) and [effects](../../build/react-to-changes.md).

## A delivery passes four stages

The node's documents pass through rules, validators, outputs and effects, in that order:

```mermaid
flowchart LR
    docs["Documents"] --> rules{"Change rules and validators"}
    rules -->|refuse| refused["Delivery refused, nothing changed"]
    rules -->|pass| render["Render and check outputs"]
    render -->|fail| refused
    render -->|pass| write["Write slot, storage, identity"]
    write --> swap["Swap outputs in"]
    swap --> effects["Take due effects"]
```

The diagram shows the two stages that can refuse a delivery before it changes anything, and the
three that follow once it passes both. In detail, a delivery to the running image, a
[`chalkctl apply-config`](../../reference/cli/chalkctl_apply-config.md), runs these steps:

1. chalkd checks the documents against the image's schemas and its change rules, then runs the
   image's validators in the renderers' sandbox. A rule that fails, or a validator that reports
   refusals or fails, refuses the delivery.
2. chalkd records the effects' baseline, unless one exists: the running slot's config, the facts
   the effects' `when` rules read and the hash of each output's files.
3. chalkd renders every output from the new documents into a staging directory below
   `/run/chalkos/outputs/.staging`, then runs each output's check. A renderer that reports
   refusals, or a renderer or check that fails, refuses the delivery, and nothing on the node has
   changed. The refusal shows what the renderers reported, each refusal once, and names any other
   failed output with its exit status.
4. chalkd writes the config slot, the storage and the node's identity.
5. chalkd swaps each output's link to its staged files.
6. chalkd takes the effects whose outputs or kinds changed, or whose `when` holds.

`apply-config` prints the units the effects restarted.

A dry run, `chalkctl apply-config --dry-run`, runs the checks of steps 1 and 3, then works out
step 6 without taking it. It records no baseline and no output failure, discards the staged files
and answers with the plan: the outputs whose files would change, and the effects step 6 would take
with what fires each. Effects whose `when`
compares facts before and after the delivery see the facts as they are, since nothing changed.

## The baseline survives a failed delivery

A delivery can fail after step 4, as when a restart fails. Until a delivery is applied whole, the
effects compare with the baseline. It holds the state from before the first delivery that failed
part way, in `/run/chalkd/effects-baseline.json`, readable by root alone, with `Secret` values as
their hashes. A retry, or another delivery, renders again and takes every effect of the failed
delivery again, the reloads and restarts that succeeded included, because what it compares with
predates them. The delivery that succeeds removes the file. A delivery refused before it changed
anything leaves the file as it was.

The file also records each delivery since the baseline as an attempt, including a delivery without
config changes, which can change a fact alone. Before step 4, chalkd adds the delivery's config and
the hashes of its outputs and kinds. Once the delivery has made its changes, chalkd adds the facts
that the effects' `when` rules read. The next delivery compares with every attempt as well as with
the baseline. A delivery of the config from before the failure therefore takes the effects that the
failed delivery took on its own config. This also holds when chalkd stopped part way through a
delivery, because the attempt is recorded first. In the comparison with an attempt, `oldConfig`
and `oldFacts` hold the attempt's config and facts. An attempt without facts, such as one that
chalkd stopped before it loaded them, makes every `when` that reads a fact hold. A retry that reads
the same facts as an earlier run replaces that run's attempt. The file keeps the 16 newest
attempts.

A delivery without config, such as a Kubernetes share, the OS CAs or a node certificate, renders
from the running slot's documents and does not change them. It runs no validator. An output that
fails then goes to the status and the journal, the delivery goes ahead, and the output keeps the
files it served.

## Every boot renders the outputs again

`/run` is a tmpfs, so no rendered file survives a reboot. `chalkos-config.service` renders every
output from the booted slot's documents before services start, runs the checks, and swaps in each
output that passes. The outputs render for the node's name, the subject of its node certificate on
STATE. A node certificate that cannot be read fails `chalkos-config.service` before any output
renders.

A renderer or check that fails fails `chalkos-config.service` once the rest of the config is
applied, and so fails the boot's [health check](boot-and-rollback.md). A boot that the boot loader
counts then falls back to the previous image and its config. A boot that was blessed already comes
up degraded: the output holds no files, its services read none, and `chalkctl status` names the
output.

## An image that does not run checks its own config

A staged image's validators and renderers live in its own store. Before an upgrade writes the
image's config slot, and for `chalkctl apply-config --staged`, the node mounts that store
read-only and runs the image's own `chalkd check` in a transient unit, `chalkos-check.service`,
with the image's store in place of its own. The checker runs every check of a delivery against
the image's schemas, rules, validators and renderers. A refusal refuses the upgrade or the
delivery before the image's config slot is written. The image renders its outputs again at its
first boot, from the same documents.

The checker gets the documents with their Secret values hashed, as renderers always do, and
asks the running chalkd for the facts its rules read. chalkd loads the facts the image
declares, and runs their commands from the image's store. The unit runs as a user of its own,
without capabilities, network or access to STATE, `/var` and `/run/chalkos`, within 512 MiB
and two minutes. What its validators, renderers and checks write to standard error is in the
unit's journal: `chalkctl logs <node> --unit=chalkos-check.service`.

An install in place runs the running image's validators, and renders and checks the outputs with
its renderers, before it installs anything, and the boot that follows renders them again. An
install from the installer checks the config with the installed image's schemas and rules,
evaluated by the installer's chalkd, but cannot run that image's programs. That image renders its
outputs at its first boot alone, and runs its validators first at the next delivery.

## Limits

- A renderer reads documents and the node's name alone. A service that needs node state reads that
  part itself.
- chalkctl cannot run an image's renderers, so `chalkctl apply-config --diff` shows the documents
  a delivery changes, not the rendered files. It cannot run the validators an image declares
  either, so `chalkctl validate` accepts a config that a validator refuses on the node.
- chalkctl runs only the rules that read no facts. The others run on the node.
- An effect's `when` compares documents as written, so a change that makes no difference to the
  unit still fires the effect, such as a value spelt another way that means the same.

## Related pages

- [Guard changes with rules and facts](../../build/guard-changes.md) for the rules and facts an
  image declares.
- [Validate config with a program](../../build/validate-config.md) for validators.
- [Render config files](../../build/render-config.md) for outputs, their checks and their
  sandbox.
- [React to changes](../../build/react-to-changes.md) for effects and the order chalkd takes them
  in.
- [Config and the inventory](config.md) for how documents reach a node.
