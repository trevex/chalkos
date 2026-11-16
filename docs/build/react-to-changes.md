---
title: "React to changes"
description: "Restart, reload or run a command when a delivery changes an output, a kind's documents or a condition"
---

# React to changes

This page is for an image author whose service must pick up new config without a reboot. At the
end a delivery reloads or restarts the service when its config changes, and leaves it alone when
nothing it reads changed. The page continues the homelab's BGP extension: bird reloads when
`bird.conf` changes.

An effect is what a delivery does to the node's services once it is applied: restart a unit,
reload it or run a command. A grant or an output never restarts a unit by itself. Which changes
matter to a service is the service's to say, in an effect.

## Before you begin

You need a service that reads config from the node: its documents through a grant, as
[Read config in a service](read-config.md) sets up, or a rendered file, as
[Render config files](render-config.md) sets up.

## Reload bird when its config changes

`chalkos.effects.<name>` declares an effect. Here is bird's, as `extensions/bgp.nix` declares it:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:effect"
```

`chalkosLib.reloadOn` takes the triggers and the units. The line above is the same effect as:

```nix
chalkos.effects.bird = {
  outputs = [ "bird-conf" ];
  kinds = [ ];
  reload = [ "bird.service" ];
};
```

Once a delivery is applied, chalkd reloads `bird.service` when the files of `bird-conf` changed.
bird's `ExecReload` runs `birdc configure`, which reads the config again and keeps the sessions
that a restart would drop. A delivery that leaves `bird.conf` and its password unchanged reloads
nothing.

`chalkosLib.restartOn` builds an effect that restarts its units instead. The rack unit of the
[extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions) reads its
`Rack` when it starts, so a changed `Rack` restarts it:

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
service reads the value as a credential beside its files. bird's password reaches it that way, as
[Pass secrets to a service](pass-secrets.md) sets up, so a new password reloads bird although
`bird.conf` names the same constant.

`kinds` cannot name `Secret` or a withheld kind, such as `Addon`. A `Secret` counts through the
kinds whose documents reference it, and chalkd reads a withheld kind's documents itself.

Delivery is idempotent: a delivery that changes nothing an effect compares takes no effect. The
same config delivered twice reloads bird once. A delivery that fails part way leaves a baseline,
and the next delivery compares with it, so it takes every effect of the failed one again. The
next delivery also compares with the config and facts of the failed delivery, so a delivery of
the config from before it takes the effects that undo it.
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
active and `try-restart` runs it again. The extensions example's rack unit does.

## Fire the effect on a condition

`when` holds a CEL expression, the language of [change rules](guard-changes.md). It fires an
effect on a condition that neither an output nor a whole kind expresses. Say a BGP exporter reads
the peers' addresses when it starts, and never their passwords. An effect on `kinds = [ "Peer" ]`
would restart it whenever a password rotates. This effect restarts it only when the addresses
change:

```nix
# The exporter reads the peers' addresses alone, so a new password restarts nothing.
chalkos.effects.bgp-exporter = {
  when = ''
    cel.bind(api, "bgp.example.org/v1alpha1",
      (api in config && "Peer" in config[api] ? config[api].Peer.map(n, config[api].Peer[n].spec.address) : [])
        != (api in oldConfig && "Peer" in oldConfig[api] ? oldConfig[api].Peer.map(n, oldConfig[api].Peer[n].spec.address) : []))
  '';
  restart = [ "bgp-exporter.service" ];
};
```

`config` maps apiVersion, kind and name to each document. A node without `Peer` documents has no
such key, so the expression checks with `in` before it reads one. A map iterates in the order of
its keys, so both lists of addresses follow the peers' names.

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

A test of a change checks which effects it takes, before any node gets the image. The homelab's
test `bgp-new-password` changes the password of the `Peer`'s `Secret` alone, and expects the
effect `bird`. `bird.conf` stays the same, but the `outputs` trigger counts a changed value of a
`Secret` that the output's input documents reference:

```nix
chalkos.tests.bgp-new-password = {
  before = ./bgp-examples/fabric.yaml;
  config = ./tests/fabric-new-password.yaml;
  outputs.bird-conf = ./tests/fabric/bird-conf;
  effects = [ "bird" ];
};
```

`nix flake check` runs it, and fails with `FAIL bgp-new-password: effects differ:` and the effects
expected and taken when the effect does not fire. [Effects are those of a
change](test-an-extension.md#effects-are-those-of-a-change) describes such tests.

On a node, `chalkctl config apply` prints each unit it restarted, as `restarted <unit>`, then the
config the node runs. Reloaded units are not listed. Check bird's journal for the reload:

```sh
chalkctl config apply node1
chalkctl logs node1 --unit=bird.service
```

## If it fails

A reload, command or restart that fails fails the delivery. The delivery is applied by then, so
the error ends with what is still to do: for example `reload bird.service by hand once that is
fixed, or deliver it again`.

A warning that starts with `the rule of effect` names a `when` that could not be evaluated, whose
effect chalkd took all the same. A unit that did not restart was not running: an effect without
`start` leaves a stopped unit stopped.

## What next

- [Pass secrets to a service](pass-secrets.md) delivers bird's password, whose rotation fires
  bird's effect.
- [Guard changes with rules and facts](guard-changes.md) refuses a change before any effect.
- [Render config files](render-config.md) for the outputs an effect names.
- [Test an extension](test-an-extension.md) for tests of the effects of a change.
- [The config pipeline](../how-it-works/node/config-pipeline.md) for where effects sit in a
  delivery.
