---
title: "React to changes"
description: "Restart, reload or run a command when a delivery changes an output, a kind's documents or a condition"
---

# React to changes

This page is for an image author whose service must pick up new config without a reboot. At the
end a delivery reloads or restarts the service when its config changes, and leaves it alone when
nothing it reads changed. The examples use the unit `hardware-info` of the
[extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions), which
reads the environment file that the output `hardware` renders.

An effect is what a delivery does to the node's services once it is applied: restart a unit,
reload it or run a command. A grant or an output never restarts a unit by itself. Which changes
matter to a service is the service's to say, in an effect.

See it in a full extension: [Reload bird on a change](../guides/bgp-extension.md#reload-bird-on-a-change)
of the BGP guide reloads bird and tests the effect of a new password.

## Before you begin

You need a service that reads config from the node: its documents through a grant, as
[Read config in a service](read-config.md) sets up, or a rendered file, as
[Render config files](render-config.md) sets up.

## Restart a unit when its file changes

`chalkos.effects.<name>` declares an effect. Here is `hardware-info`'s, with the unit, as
`hardware.nix` declares them:

```nix title="hardware.nix"
--8<-- "examples/extensions/hardware.nix:tail"
```

`chalkosLib.restartOn` takes the triggers and the units. The effect above is the same as:

```nix
chalkos.effects.hardware-info = {
  outputs = [ "hardware" ];
  kinds = [ ];
  restart = [ "hardware-info.service" ];
};
```

Once a delivery is applied, chalkd restarts `hardware-info.service` when the files of `hardware`
changed. A delivery that leaves `hardware.env` unchanged restarts nothing.

`chalkosLib.reloadOn` builds an effect that reloads its units instead, for a service whose
`ExecReload` reads its config again without a restart:

```nix
chalkos.effects.myapp = chalkosLib.reloadOn { outputs = [ "myapp-conf" ]; } [ "myapp.service" ];
```

The rack unit of the extensions example reads its `Rack` document itself when it starts, so its
effect names the kind:

```nix
chalkos.effects.rack-location = chalkosLib.restartOn { kinds = [ "Rack" ]; } [
  "rack-location.service"
];
```

## Choose what fires the effect

An effect fires when any of its triggers fires. It has at least one, or the image's evaluation
fails:

| Trigger | Fires when |
| --- | --- |
| `outputs` | The hash of one output's files differs from the one before the delivery, or the values of the Secrets its input documents reference differ. |
| `kinds` | A document of one kind was added, removed or changed, or a value of a Secret one of them references changed. |
| `when` | The CEL expression holds once the delivery is applied. |

Use `outputs` for a service that reads a rendered file, and `kinds` for a service that reads its
documents itself when it starts. Both count a rotated `Secret` value as a change, because the
service reads the value as a credential beside its files. A new password therefore restarts or
reloads the service, although its rendered file names the same credential, as
[Pass secrets to a service](pass-secrets.md) sets up.

`kinds` cannot name `Secret` or a withheld kind, such as `Addon`. A `Secret` counts through the
kinds whose documents reference it, and chalkd reads a withheld kind's documents itself.

Delivery is idempotent: a delivery that changes nothing an effect compares takes no effect. The
same config delivered twice restarts `hardware-info` once. A delivery that fails part way leaves
a baseline, and the next delivery compares with it, so it takes every effect of the failed one
again. The next delivery also compares with the config and facts of the failed delivery, so a
delivery of the config from before it takes the effects that undo it.
[The config pipeline](../how-it-works/node/config-pipeline.md#the-baseline-survives-a-failed-delivery)
describes the baseline.

## Choose what the effect does

An effect takes at least one action of `reload`, `restart`, `run` and `chalkd`, or the image's
evaluation fails. `start` changes what `restart` does, and `hint` what a failed restart reports:

| Option | Action |
| --- | --- |
| `reload` | Reloads the units with `systemctl reload`, by their full names. A unit that failed it restarts instead, since systemd reloads no failed unit. |
| `restart` | Restarts the units, by their full names. A unit that does not run stays stopped. |
| `start` | With `restart`, starts the units that do not run too. |
| `run` | Runs one command after the reloads and before the restarts. |
| `chalkd` | Runs a layer's action on what chalkd itself holds, named `<layer>:<action>`, such as `kubernetes:restart`. The image must carry the layer; the note below covers the actions. |
| `hint` | Why the effect's units may fail to restart: `text`, and a `file` whose content follows it, which the delivery's error adds to systemctl's. |

`start` is for a unit that only the delivery lets start, such as a unit whose condition the
delivered config meets for the first time. Without it, a stopped unit stays stopped.

`run` names a program and its arguments. chalkd splits the command on spaces, with no shell
quoting, and runs its first word: a program of the image's store by its absolute path, or a bare
name on chalkd's `PATH`. A module adds a program to that `PATH` with
`systemd.services.chalkd.path`, as chalkos's chrony effect does. chalkd runs the command whether
or not any unit runs. A command that fails fails the delivery, so a command that talks to a
service that may be stopped first checks that the service runs.

A layer adds subcommands to chalkd, run as `chalkd <layer> <command>`, such as
`chalkd kubernetes registry-credentials`. The image's units run them, and an effect's `run` may
too. The evaluation fails on a `run` of `chalkd <layer> ...` or a `chalkd` action whose layer the
image does not carry.

`hint` is for a unit whose failure systemctl explains badly, such as a oneshot unit that writes
why it failed to a file. When a unit of `restart` fails, the delivery's error adds `text` after
systemctl's error, followed by the content of `file`. An effect with a `file` adds its hint only
while the file holds text. chalkos's own `kubernetes` effect names `prepare.error`, so a delivery
whose preparation fails says why.

The image build fails on an effect that restarts or reloads a unit the image does not define, and
the evaluation on one whose `outputs` names no output.

chalkd takes the actions of every due effect together, every reload before any restart.
[chalkd takes the due effects in one order](../how-it-works/node/config-pipeline.md#chalkd-takes-the-due-effects-in-one-order)
gives the order, and what the Kubernetes layer's `chalkd` actions do.

A oneshot unit that should run again on a change sets `RemainAfterExit = true`, so it stays
active and `try-restart` runs it again. `hardware-info` and the rack unit both do.

## Order effects and wait for readiness

Use `after` when an effect needs another effect from the same delivery to succeed first. An
unchanged prerequisite does not run. Use `waitFor` to check the runtime state a consumer
needs, including when its prerequisite has no changes. For example, a router's resolver effect
waits for the VRFs it uses to exist with their configured routing tables.

```nix
chalkos.effects.resolver = {
  outputs = [ "resolver" ];
  reload = [ "resolver.service" ];
  after = [ "networkd" ];
  waitFor = {
    description = "the resolver's link to be configured";
    argv = [ (lib.getExe resolverLinkReady) ];
    timeoutSeconds = 15;
  };
};
```

The probe is a read-only executable from the image's Nix store. It reads the live node and
gets no config or credentials from chalkd. It exits with 0 when ready, 75 to retry, or another
nonzero status to fail the effect. Chalkd runs one bounded attempt at a time and stops at the
configured timeout. The description is shown in the plan and on
failure, so it should name the condition rather than a command.

The image rejects cycles and missing prerequisites. Effects sharing a unit or command run
together, so a dependency between them is invalid. A restart still subsumes a reload of the
same unit. Failed or timed-out effects block their dependents, and independent effects can
continue. `chalkctl config apply --dry-run` shows scheduled effects, their due prerequisites,
and their readiness checks. It does not run the checks.
An operator can [override a failed readiness check](../operate/change-config.md#override-a-failed-runtime-readiness-check)
for one delivery.

## Fire the effect on a condition

`when` holds a CEL expression, the language of [change rules](guard-changes.md). It fires an
effect on a condition that neither an output nor a whole kind expresses. Say an inventory agent
reports the server's model when it starts, and never its asset tag. An effect on
`kinds = [ "Hardware" ]` would restart it whenever the asset tag changes. This effect restarts it
only when the model changes:

```nix
# The agent reports the model alone, so a new asset tag restarts nothing.
chalkos.effects.inventory-agent = {
  when = ''
    cel.bind(api, "hardware.example.org/v1alpha1",
      (api in config && "Hardware" in config[api] ? config[api].Hardware.map(n, config[api].Hardware[n].spec.model) : [])
        != (api in oldConfig && "Hardware" in oldConfig[api] ? oldConfig[api].Hardware.map(n, oldConfig[api].Hardware[n].spec.model) : []))
  '';
  restart = [ "inventory-agent.service" ];
};
```

`config` maps apiVersion, kind and name to each document. A node without `Hardware` documents has
no such key, so the expression checks with `in` before it reads one. A map iterates in the order
of its keys, so both lists follow the documents' names.

The expression reads the config and facts as they are once the delivery is applied, and as they
were before it. [Effect conditions](../reference/extension-runtime.md#effect-condition-variables)
lists the variables. A fact is node state that a rule reads, such as the JSON of a file.
[Read node state from a fact](guard-changes.md#read-node-state-from-a-fact) declares one.

## Built into chalkos

Every image renders the node's network and time servers as outputs of its own, `networkd` and
`chrony`. Their names are taken, so an extension's outputs and effects use others.
[Built-in outputs and effects](../how-it-works/node/config-pipeline.md#built-in-outputs-and-effects)
describes them and the effects images with Kubernetes add.

## Check that it worked

A test of a change checks which effects it takes, before any node gets the image. Here `before`
holds a `Hardware` with the asset tag `HL-0042`, and `config` the same document with `HL-0043`.
`hardware.env` changes, so `hardware-info` fires, and the model stays, so `inventory-agent` does
not:

```nix
chalkos.tests.hardware-new-asset-tag = {
  before = ./hardware-examples/server.yaml;
  config = ./tests/new-asset-tag.yaml;
  effects = [ "hardware-info" ];
};
```

`nix flake check` runs it, and fails with `FAIL hardware-new-asset-tag: effects differ:` and the
effects expected and taken when they differ. A config with a new model takes both:

```console
$ cat result/hardware-new-model/effects.json
["hardware-info","inventory-agent"]
```

[Effects are those of a change](test-an-extension.md#effects-are-those-of-a-change) describes such
tests.

On a node, `chalkctl config apply` prints each unit it restarted, as `restarted <unit>`, then the
config the node runs. Reloaded units are not listed. In the lab of
[Write your first extension](first-extension.md#change-the-document-on-w1), a new asset tag
restarts the unit:

```console
$ chalkctl config apply w1 --secrets secrets.json
w1: config 0 added, 0 removed, 1 changed
restarted hardware-info.service
w1 runs config 9307d1da8a3a17a8e611ded2cdbdd577692950cc6eb963ce1712ecf656a25416
```

For a reloaded unit, its journal shows the reload:

```sh
chalkctl logs server1 --unit=myapp.service
```

## If it fails

A reload, command or restart that fails fails the delivery. The delivery is applied by then, so
the error ends with what is still to do: for example `reload myapp.service by hand once that is
fixed, or deliver it again`.

A warning that starts with `the rule of effect` names a `when` that could not be evaluated, whose
effect chalkd took all the same. A unit that did not restart was not running: an effect without
`start` leaves a stopped unit stopped.

## What next

- [Pass secrets to a service](pass-secrets.md) delivers a value whose rotation fires the
  effects.
- [Guard changes with rules and facts](guard-changes.md) refuses a change before any effect.
- [Render config files](render-config.md) for the outputs an effect names.
- [Test an extension](test-an-extension.md) for tests of the effects of a change.
- [The config pipeline](../how-it-works/node/config-pipeline.md) for where effects sit in a
  delivery.
