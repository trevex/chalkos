---
title: "Customise an image"
description: "Add packages, services, kernel modules, variants and config kinds of your own to a cluster's images"
---

# Customise an image

A cluster's images are built by `chalkos.lib.mkImages` from chalkos's node modules, each
[platform](../reference/glossary.md#platform)'s modules and NixOS modules of your own, which you
give every [variant](../reference/glossary.md#variant) with
[`modules`](../reference/options.md#modules) or one variant with
[`variants.<name>.modules`](../reference/options.md#variantsmodules). They can add packages,
services, kernel modules and anything else a NixOS module sets, within the limits an image-based
node sets, and they can declare [config kinds](../reference/glossary.md#kind) whose documents give
each node its own values. Every node of a variant and platform runs the same image, so a value that
differs between nodes travels in the node's YAML [config](../reference/glossary.md#config), never in
the image. A change to the modules reaches running nodes as an
[upgrade](../reference/glossary.md#upgrade) to the new image, which a new
[root hash](../reference/glossary.md#root-hash) identifies. Kubernetes objects for the cluster are
`Addon`
documents in the YAML and need no image change; [Kubernetes on chalkos](../concepts/kubernetes.md)
describes them.

## Before you begin

You need a flake that builds the cluster's images with `mkImages`, such as the lab template's or the
[homelab example](https://github.com/trevex/chalkos/tree/main/examples/homelab)'s, and its
[inventory](../reference/glossary.md#inventory), `chalkos.yaml`, with the nodes' config files. To
roll the change out you need a running cluster and an operator [client
file](../reference/glossary.md#client-file) or the [secrets
file](../reference/glossary.md#secrets-file) for the upgrade, and the secrets file to deliver
changed YAML. [Variants and platforms](../concepts/variants.md) explains how `mkImages` builds the
images, [Config and the inventory](../concepts/config.md) how the YAML reaches the nodes, and [The
image](../concepts/image.md) what an image holds.

This guide starts from the homelab example: one variant, `node`, built for `metal`, whose image
both nodes run.

## Add a module to every variant

A module of the images is an ordinary NixOS module. This one runs a service on every node, with
the tools it calls in its own `path`:

```nix title="modules/disk-report.nix"
{ pkgs, ... }:
{
  systemd.services.disk-report = {
    wantedBy = [ "multi-user.target" ];
    path = [ pkgs.smartmontools ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${pkgs.smartmontools}/bin/smartctl --scan";
    };
  };
}
```

Add it to `modules` in `flake.nix`, which every variant's images carry after their platform's
modules and before the variant's own:

```nix
images = chalkos.lib.mkImages {
  platforms = [ "metal" ];
  modules = [ ./modules/disk-report.nix ];
  variants.node.modules = [ ./extensions/rack.nix ];
};
```

Nodes have no logins, so the system path holds only what the image's modules put there. Name
the tools a unit runs in its `path`, as above. `environment.systemPackages` works as on any NixOS
system and is the place for what other programs look up there, such as a mount helper
(`mount.nfs`) that a CSI driver runs.

Modules that belong to a kind of machine go into a platform;
[Support additional hardware](additional-hardware.md#define-a-platform-of-your-own) defines one. A
platform's modules come before the others, but NixOS merges a value by priority, not by module
order: a module of yours overrides a value its platform sets plainly with `lib.mkForce`, and one
the platform sets with `lib.mkForce` itself, such as the `kvm` platform's `ExecStart` of the
guest agent, with `lib.mkOverride` below 50, for example `lib.mkOverride 40`.

## Add kernel modules and debug tools

A module adds [module groups](../reference/glossary.md#module-group) and single kernel modules
with [`chalkos.kernel.moduleGroups`](../reference/options.md#chalkoskernelmodulegroups) and
[`chalkos.kernel.extraModules`](../reference/options.md#chalkoskernelextramodules):

```nix
{
  chalkos.kernel.moduleGroups = [ "gpu" ];
  chalkos.kernel.extraModules = [ "kvm_amd" ];
}
```

[Support additional hardware](additional-hardware.md) finds the module a device needs and adds
firmware and out-of-tree modules.

[`chalkos.debug.tools`](../reference/options.md#chalkosdebugtools) adds the usual command-line
tools for debugging from a privileged pod, as
[Troubleshooting](troubleshooting.md#open-a-shell-on-a-node) shows.

## Build a variant for some nodes

A module that only some nodes need, such as a GPU driver, goes into a variant of its own, so the
other nodes' images stay without it. Write the variant's module:

```nix title="modules/gpu.nix"
{
  chalkos.kernel.moduleGroups = [ "gpu" ];
}
```

Then declare the variant next to `node` in `flake.nix` and expose its image:

```nix title="flake.nix"
{
  description = "A chalkos home lab cluster";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    let
      images = chalkos.lib.mkImages {
        platforms = [ "metal" ];
        # Every variant carries these, so nodes of both accept Rack documents.
        modules = [
          ./modules/disk-report.nix
          ./extensions/rack.nix
        ];
        variants = {
          node = { };
          gpu.modules = [ ./modules/gpu.nix ];
        };
      };
    in
    {
      packages.x86_64-linux = {
        node-metal = images.images.node-metal;
        gpu-metal = images.images.gpu-metal;
      };
    };
}
```

`mkImages` builds each variant for each platform as `images.<variant>-<platform>`, so this flake
builds `node-metal` and `gpu-metal`. A variant's name becomes `CHALKOS_VARIANT` in its images'
os-release. [`variants.<name>.kubernetes`](../reference/options.md#variantskubernetes), `true`
by default, decides whether its images run Kubernetes; control planes and workers share one
variant, and each node's `Node` document makes it one or the other.

A node runs the image its `Node` document's `imageRef` names. Add an `Image` document for the new
variant to `cluster/cluster.yaml`, which every node gets, beside the one for `node-metal`:

```yaml
apiVersion: chalkos.dev/v1alpha1
kind: Image
metadata: {name: gpu-metal}
spec:
  # nix build .#gpu-metal -o images/gpu-metal puts the image here.
  source: {directory: ../images/gpu-metal}
```

Then point the node that has the GPU at it, in its own file, here `nodes/w1.yaml`:

```yaml title="nodes/w1.yaml"
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: w1}
spec:
  type: worker
  imageRef: gpu-metal
```

The other documents of `nodes/w1.yaml` stay as they are. A node's config may hold several `Image`
documents; chalkctl reads only the image directory of the one its `Node` names.

## Read a node's config in a unit

A unit that needs one of the node's values reads its config documents. It names the kinds it
reads under [`chalkos.consumers`](../reference/options.md#chalkosconsumers):
[chalkd](../reference/glossary.md#chalkd) writes their documents to
`/run/chalkos/config/<group>/<Kind>/<name>.json` and restarts the unit when one of them changes.
The node's `Node` document gives its type, for example:

```nix
{ config, pkgs, ... }:
{
  systemd.services.node-type = {
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      # Active after it ran, so the restart on a change runs it again.
      RemainAfterExit = true;
      ExecStart = pkgs.writeShellScript "node-type" ''
        echo "this node is a $(${config.chalkos.kinds.Node.get { path = "spec.type"; }})"
      '';
    };
  };
  chalkos.consumers.node-type.kinds = [ "Node" ];
}
```

[`chalkos.kinds.<Kind>.get`](../reference/options.md#chalkoskindsget) returns a
[chalkcfg](../reference/glossary.md#chalkcfg) command, here
`/nix/store/…/bin/chalkcfg get Node -o 'jsonpath={.spec.type}'`, which prints the field for each
document of the kind, one per line. `name` picks one document, and `output` asks for `json`,
`yaml`, `jsonpath=<expression>` or `go-template=<template>` in place of a `path`. A path follows
the kind's options: fields separated by dots, `*` or an index for the elements of a list, and a
key or `*` for the values of an attribute set. Evaluating the image fails on a path the kind
does not have, and the message lists the fields at that point:

```text
chalkos.kinds.Node.get: the path "spec.typ" does not follow the kind's options: spec has no field typ; its fields are hostname, imageRef, type
```

The check covers the kind's options, not the documents. A document that leaves the path's field out,
or whose list is shorter than the index, makes the command exit with status 1, and without `name`
one such document fails the whole command; a field set to null prints `null`. `*` over an attribute
set prints its values in no particular order; a `go-template` output that ranges over the keys
prints them sorted. A key that contains `.`, whitespace or one of `'`, `\`, `@`, `$`, `,`, `[`, `]`,
`{` and `}` cannot be part of a path, and only a `go-template` output reads it.

Run the command from a shell script, as `pkgs.writeShellScript` makes one, because systemd
expands `$` and `%` in `ExecStart=` itself. Without `name`, a kind the image declares but the
node has no document of gives empty output and exit status 0, so the unit also runs on nodes
without one; with `name`, a missing document makes the command exit with status 1;
[`chalkcfg get`](../reference/cli/chalkcfg_get.md) lists the exit statuses. The documents are
also files, one per document below the kind's
[`dir`](../reference/options.md#chalkoskindsdir).

networkd's `Network`, `NetDev` and `Link` have no `get`. A section of theirs may be an object or
a list, and their schemas come from systemd's tables at build time, so a path through them
cannot be checked when the image is evaluated. chalkcfg is on the `PATH` of every unit under
`chalkos.consumers`, so such a unit runs it directly:

```sh
chalkcfg get Network -o json
```

Evaluating the image fails when a unit names a kind the image does not declare. A `secretRef`
field of a kind the unit reads names a key of a `Secret` document; the unit reads its value as
the systemd credential `secret_<secret>.<key>`, in `$CREDENTIALS_DIRECTORY` or `%d`. chalkcfg
never prints `Secret`s, and their values never appear below `/run/chalkos/config`. Set
[`restart.when`](../reference/options.md#chalkosconsumersrestartwhen) to `"false"` for a unit
that rereads its documents itself.

## Add a config kind of your own

A value of your own per node, such as where a machine is mounted, needs a kind of its own: an
[extension](../reference/glossary.md#extension) declares it in a module of the images, and each
node's YAML holds a document of it. The homelab's `Rack` kind records a node's rack position and
hands it to a unit:

```nix title="extensions/rack.nix"
# The Rack kind, which records a node's rack position, and a unit that reads it on the node. A
# variant that carries this module accepts Rack documents. A node that was surveyed, whose
# rack-survey tool wrote where it found the node, takes no other location than that once it runs;
# an install reads no facts and takes any.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  chalkos.kinds.Rack = {
    group = "rack.example.org";
    version = "v1alpha1";
    spec.location =
      lib.mkOption {
        type = lib.types.nonEmptyStr;
        example = "rack-a/u12";
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
  };
  # What the rack survey found, {"location": "rack-a/u12"}; null on a node not surveyed yet.
  chalkos.facts.rackSurvey.file = "/var/lib/rack-survey/location.json";
  # chalkd writes the node's Rack below /run/chalkos/config and restarts the unit when it changes.
  chalkos.consumers.rack-location.kinds = [ "Rack" ];
  systemd.services.rack-location = {
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      # A path the kind lacks fails the image's evaluation.
      ExecStart = pkgs.writeShellScript "rack-location" (
        config.chalkos.kinds.Rack.get {
          name = "rack";
          path = "spec.location";
        }
      );
    };
  };
}
```

[`chalkos.kinds`](../reference/options.md#chalkoskinds) declares the kind's group, version and
`spec` as NixOS options, from which the image build writes the kind's JSON Schema. The group is
a DNS name of your own, since chalkos keeps `chalkos.dev` and its subdomains for its own kinds.
Put the module where the nodes that carry the kind's documents get it: in `modules` when every
variant accepts it, as in the flake above, or in one variant's `modules`. A node whose image lacks
the kind refuses a config that holds a document of it.

A node's config holds at most one document of a kind like `Rack`. A kind of which a node holds
several, each with a name of its own, sets `multiple`, here one document per BGP neighbour:

```nix
chalkos.kinds.Peer = {
  group = "bgp.example.org";
  version = "v1alpha1";
  multiple = true;
  spec.address = lib.mkOption {
    type = lib.types.nonEmptyStr;
    description = "Address of the neighbour.";
  };
};
```

Without `name`, `config.chalkos.kinds.Peer.get { path = "spec.address"; }` builds a command that
prints the field of every `Peer` document, one line each.

Give each node its document in its own file, here `nodes/cp1.yaml`:

```yaml
apiVersion: rack.example.org/v1alpha1
kind: Rack
metadata: {name: rack}
spec:
  location: rack-a/u12
```

A change of a node's position needs no new image:
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) delivers the new `Rack`
document, and chalkd restarts `rack-location.service`.
[Variants and platforms](../concepts/variants.md#how-does-an-extension-add-a-kind) explains how
an extension's kind reaches the image and the node.

## Add a rule and a fact to a kind

A rule decides which changes of your kind's documents a node accepts, and a fact gives it what
the node knows. `rack.nix` above declares both: the `x-chalkos-rules` of `spec.location` refuse a
location that differs from the one a rack survey wrote to the node, and
[`chalkos.facts`](../reference/options.md#chalkosfacts) declares that file as the fact
`rackSurvey`. The rule is CEL: `self` is the delivered location and `facts.rackSurvey` the file's
JSON, null while the file is missing. The rule does not read `oldSelf`, the location the node runs
with, so it checks every `Rack` a node is delivered, its first one too. An install reads every
fact as null, so it takes any location.

A rule over all of a kind's documents goes into `chalkos.kinds.<name>.rules`, where `documents`
maps the names of the delivered documents to them, here allowing two `Peer` documents at most:

```nix
chalkos.kinds.Peer.rules = [
  {
    rule = "size(documents) <= 2";
    message = "a node has two BGP neighbours at most";
  }
];
```

A fact can also be a command of the image's store, which reads the config as JSON on standard
input and prints the fact as JSON. The image build compiles every rule and fails on one that
does not compile or reads a fact no module declares. Say the survey file on `cp1` holds
`{"location": "rack-b/u3"}`, because the node was moved to another rack, while `nodes/cp1.yaml`
above still says `rack-a/u12`. `chalkctl apply-config cp1` then refuses the config:

```text
the config is refused: Rack "rack" spec.location: the survey found the node at rack-b/u3, not at rack-a/u12; fix the location, or survey the node again
```

The survey and the document disagree, so the node refuses its config until they agree: set
`location: rack-b/u3` in `nodes/cp1.yaml` where the survey is right, or survey the node again
where the document is.

`chalkctl validate` runs the rules that read no facts, such as the `Peer` rule; the rack rule
runs on the node. [Change rules and facts](../concepts/rules.md) explains the variables, the
functions and where rules run.

## Choose what a change restarts

chalkd restarts a unit under `chalkos.consumers` when a document of its kinds changes, or a
`Secret` one of them references.
[`restart.when`](../reference/options.md#chalkosconsumersrestartwhen) replaces that rule with a CEL
expression of your own, and [`reload`](../reference/options.md#chalkosconsumersreloadwhen) reloads
the unit instead. A BGP daemon reads its `Peer` documents again on a reload, which keeps the
sessions a restart would drop:

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

A rule reads the config before and after the delivery as `oldConfig` and `config`, the names of
the kinds that changed as `changedKinds`, and the facts before and after it as
`oldFacts.<name>` and `facts.<name>`. A unit that reads no kind restarts on a fact alone, here
when the node gets a new Kubernetes share:

```nix
chalkos.consumers.cert-agent.restart.when = "facts.share != oldFacts.share";
```

The image build compiles these rules with the kinds' rules.
[Change rules and facts](../concepts/rules.md#which-units-does-a-change-restart-or-reload)
explains when chalkd evaluates them and how it restarts and reloads units.

## Know what an image may not change

The image is an appliance: the root file system is a tmpfs, `/etc` is read-only, users are fixed
at build time, and there is no Nix and no `nixos-rebuild` on the node. A service keeps its data
below `/var`, which is [VAR](../reference/glossary.md#var), or on a
[volume](../reference/glossary.md#volume). Within that, a module can set anything, and a few
settings break the node:

- chalkd, `chalkos-config.service`, `chalkos-health.service` and systemd-networkd are what
  install, configure and upgrade the node. An image that disables or replaces them builds, and
  the node then cannot be installed or upgraded.
- `mkImages` sets `chalkos.variant.name`, `chalkos.platform.name`, `chalkos.kubernetes.enable`
  and `chalkos.maintenance.osCA` from the variant's and platform's names,
  [`variants.<name>.kubernetes`](../reference/options.md#variantskubernetes) and
  [`osCA`](../reference/options.md#osca); set those arguments instead.
- The partition sizes under `chalkos.disk` are laid out when a node is installed. A changed size
  applies only to nodes installed afterwards, and an upgrade whose store does not fit a node's
  [slot](../reference/glossary.md#slot) is refused.
- On a variant without Kubernetes, a boot is healthy only when no unit that `multi-user.target`
  or `sysinit.target` pulls in failed, so such a unit of yours that fails makes the next upgrade
  roll back. Jobs that timers and sockets start never count.
  [`chalkos.upgrade.healthIgnoreUnits`](../reference/options.md#chalkosupgradehealthignoreunits)
  lists units whose failure does not count.

## Keep the image within its slot

Every package and module adds to the store, which each upgrade sends to every node. The image
build fails when the store's data takes more than 80% of its slot (3 GiB by default,
[`chalkos.disk.storeSize`](../reference/options.md#chalkosdiskstoresize)), its hash tree more than
80% of its partition, or the [UKIs](../reference/glossary.md#uki) more than 80% of the
[ESP](../reference/glossary.md#esp), so the next image always fits beside the running one.
[Support additional hardware](additional-hardware.md#keep-the-image-within-its-slot) shows the
message and what to do.

## Name the build

A changed module changes the image's store and so its root hash, which is all a node needs to take
the image as a new one; there is no version to raise, and the build refuses
`system.image.version`. To see which commit a node runs, pass it to `mkImages` in `flake.nix`,
with `self` among the outputs' arguments:

```nix
chalkos.lib.mkImages {
  revision = self.rev or self.dirtyRev or null;
  # platforms, variants and modules as before
}
```

`chalkctl status` then shows it beside the root hash, as in `3fa9c1e2b7d0 (a1b2c3d4e5f6)`, and
with a release's `tag` passed too, `3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)`.

## Build the images and check the config

Build each image where its `Image` document points, then check every node's config against the
schemas of the image it names:

```sh
nix build .#node-metal -o images/node-metal
nix build .#gpu-metal -o images/gpu-metal
chalkctl validate
```

[`chalkctl validate`](../reference/cli/chalkctl_validate.md) reaches no node: it reads each
node's files from the inventory and the schemas from the image directory its `Image` names, so a
`Rack` document on a node whose image lacks the kind, or a misspelled field, fails here.

## Roll the change out

Upgrade the nodes with [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md), which signs
each image with the cluster's [db](../reference/glossary.md#db-and-dbx) key and gives each node
the image its `Image` document names, `w1` the new `gpu-metal`:

```sh
chalkctl upgrade --sign-key=db.key --sign-cert=db.crt
```

Nodes already running their image are skipped. Each node gets its config with the image, the
changed YAML with the new kind's documents included, and checks it against the new image before
the image becomes its next boot; the image the node ran keeps its own config, which a fallback
boots with. A node may move to an image of another variant the same way.
[Upgrade a cluster](upgrade-cluster.md) covers the run, rollbacks and downtime.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) shows the image each node booted,
whether that boot was found healthy, whether it runs the image its `Image` names and the config
the inventory gives it, and names failed units. The unit's journal shows what it read:

```sh
chalkctl status w1
chalkctl logs cp1 --unit=rack-location.service
chalkctl logs w1 --unit=disk-report.service
```

`rack-location.service` prints `rack-a/u12` on `cp1`.

## What next

- [Variants and platforms](../concepts/variants.md) explains how `mkImages` builds variants and
  how an extension adds a kind.
- [Config and the inventory](../concepts/config.md) explains how the YAML reaches the nodes.
- [Support additional hardware](additional-hardware.md) adds drivers, firmware and platforms.
- [Upgrade a cluster](upgrade-cluster.md) rolls the new images out.
