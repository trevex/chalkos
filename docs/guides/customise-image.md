---
title: "Customise an image"
description: "Add packages, services, kernel modules, variants, config kinds and checked service config of your own to a cluster's images"
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
`Addon` documents in the YAML and need no image change;
[Kubernetes on chalkos](../concepts/kubernetes.md) describes them.

From [Add a config kind of your own](#add-a-config-kind-of-your-own) on, the guide builds one
extension: bird, a BGP daemon, whose neighbours are `Peer` documents, whose config file the node
renders from them and checks before it accepts a change, and which reloads when that file
changes.

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
        # Every variant carries these, so nodes of both accept Rack and Peer documents.
        modules = [
          ./modules/disk-report.nix
          ./extensions/rack.nix
          ./extensions/bird
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

A node runs the image the [inventory](../reference/kinds.md#inventory) names for it. Add the new
variant's image to the inventory's `spec.images`, beside `node-metal`, and name it on the node
that has the GPU, here `w1`:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    node-metal: {source: {directory: images/node-metal}}
    # chalkbuild sign signs a copy of the gpu-metal build into this directory.
    gpu-metal: {source: {directory: images/gpu-metal}}
  all: {imageName: node-metal, files: [cluster/*.yaml]}
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    w1: {address: 10.0.0.21, imageName: gpu-metal, files: [nodes/w1.yaml]}
```

A relative directory is relative to the inventory's directory. A node's own `imageName` wins,
then the `imageName` of its groups, then `spec.all.imageName`; a node in two groups that name
different images, or one that ends up with no image, makes chalkctl refuse the inventory. Several
GPU nodes can share a group with `imageName: gpu-metal` instead. The node's config files stay as
they are.

## Add a config kind of your own

A value of your own per node needs a config kind of its own: an
[extension](../reference/glossary.md#extension) declares the kind in a module of the images, and
each node's YAML holds documents of it. This guide builds one extension from here on: bird, a
BGP daemon, with its neighbours in a `Peer` kind, its config file rendered from the `Peer`
documents, and a reload when that file changes. Create `extensions/bird/default.nix` with the
kind first:

```nix
chalkos.kinds.Peer = {
  group = "bgp.example.org";
  version = "v1alpha1";
  multiple = true;
  spec = {
    address = lib.mkOption {
      type = lib.types.nonEmptyStr;
      example = "10.0.0.1";
      description = "Address of the neighbour.";
    };
    asn = lib.mkOption {
      type = lib.types.ints.between 1 4294967295;
      example = 64513;
      description = "AS number of the neighbour.";
    };
  };
};
```

[`chalkos.kinds`](../reference/options.md#chalkoskinds) declares the kind's group, version and
`spec` as NixOS options, from which the image build writes the kind's JSON Schema, so chalkctl
and the node refuse a misspelled field or an AS number out of range. The group is a DNS name of
your own, since chalkos keeps `chalkos.dev` and its subdomains for its own kinds. `multiple`
lets a node hold several `Peer` documents, each with a name of its own; without it, a node holds
at most one document of the kind. The whole module follows below.

Give each node its documents in its own file, here `nodes/cp1.yaml`:

```yaml
apiVersion: bgp.example.org/v1alpha1
kind: Peer
metadata: {name: upstream-a}
spec: {address: 10.0.0.1, asn: 64513}
---
apiVersion: bgp.example.org/v1alpha1
kind: Peer
metadata: {name: upstream-b}
spec: {address: 10.0.0.2, asn: 64514}
```

Put the module where the nodes that carry the kind's documents get it: in `modules` when every
variant accepts it, or in one variant's `modules`. A node whose image lacks the kind refuses a
config that holds a document of it.
[Variants and platforms](../concepts/variants.md#how-does-an-extension-add-a-kind) explains how
an extension's kind reaches the image and the node.

## Render the service's config from the documents

bird reads one config file, which an output renders from the node's `Peer` documents. An
[output](../reference/glossary.md#output) is a set of files that a program of the image renders
and checks before a delivery changes the node, so a `Peer` that would give bird a config it
rejects is refused, and the node keeps the config bird runs.
[Outputs and effects](../concepts/outputs-and-effects.md) describes the mechanism.

Write the config as a Go template, `extensions/bird/bird.conf.tmpl`:

```text title="extensions/bird/bird.conf.tmpl"
# bird's config for {{ .node.name }}, rendered by chalkos from its Peer documents.
router id from "*";

protocol device {
}
{{ range $i, $peer := .documents.Peer }}
protocol bgp peer{{ $i }} {
  description {{ quote $peer.metadata.name }};
  local as 64512;
  neighbor {{ $peer.spec.address }} as {{ $peer.spec.asn }};
  ipv4 {
    import all;
    export none;
  };
}
{{ end -}}
```

The template reads the node's name as `.node.name` and the documents of each input kind, sorted by
name and with their schema defaults, as `.documents.<Kind>`. Besides Go's own functions it has
`join`, `quote`, `json` and `default`. A key a document lacks, or that holds null, prints as
nothing, and `default "none" .spec.description` replaces it; reading a key below a missing one,
such as `.spec.missing.name`, fails the render. Declare the output in the module:

```nix
chalkos.outputs.bird-conf = {
  inputs = [ "Peer" ];
  render = lib.chalkos.render.template ./bird.conf.tmpl;
  check = "${pkgs.bird2}/bin/bird -p -c $OUT/bird.conf";
  examples = ./examples;
  user = "bird";
  group = "bird";
  mode = "0640";
};
```

- `inputs` names the kinds the renderer reads.
- `render` is the renderer that writes the files into `$OUT`.
  [`lib.chalkos.render.template`](../concepts/outputs-and-effects.md#which-renderers-does-libchalkos-offer)
  renders a template into a file named as the template less `.tmpl`, here `bird.conf`.
- `check` runs in the renderer's sandbox against the rendered files. `bird -p` parses the config
  and exits with status 1 on an error, which refuses the delivery.
- `examples` is a directory of config files that the image build renders and checks, so a template
  that does not render, or renders a config bird rejects, fails the build before any node gets
  it.
- `user`, `group` and `mode` are the owner and mode of the files, `root:root 0644` by default.

Each file of `examples` is one node's documents. Add one with two neighbours,
`extensions/bird/examples/two-peers.yaml`, holding the two `Peer` documents above, and one with
none, `extensions/bird/examples/no-peers.yaml`, for a node without neighbours:

```yaml title="extensions/bird/examples/no-peers.yaml"
apiVersion: chalkos.dev/v1alpha1
kind: Host
metadata: {name: default}
spec: {}
```

A renderer never sees a `Secret`'s value, so an output's files hold no secret. A config that
needs a password names the credential the service reads instead, as
[Read documents and secrets at runtime](#read-documents-and-secrets-at-runtime) describes.

## Run the service and reload it on a change

The service reads its config at the output's `dir`, `/run/chalkos/outputs/bird-conf` by default,
which the module names as `config.chalkos.outputs.bird-conf.dir`, so a misspelled output fails
the evaluation. An [effect](../reference/glossary.md#effect) reloads bird when the output changes.
The whole module:

```nix title="extensions/bird/default.nix"
# BGP sessions with bird: the Peer kind holds a node's neighbours, the output bird-conf renders
# bird's config from them, and bird reloads when that config changes.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  socket = "/run/bird/bird.ctl";
in
{
  chalkos.kinds.Peer = {
    group = "bgp.example.org";
    version = "v1alpha1";
    multiple = true;
    spec = {
      address = lib.mkOption {
        type = lib.types.nonEmptyStr;
        example = "10.0.0.1";
        description = "Address of the neighbour.";
      };
      asn = lib.mkOption {
        type = lib.types.ints.between 1 4294967295;
        example = 64513;
        description = "AS number of the neighbour.";
      };
    };
  };

  chalkos.outputs.bird-conf = {
    inputs = [ "Peer" ];
    render = lib.chalkos.render.template ./bird.conf.tmpl;
    check = "${pkgs.bird2}/bin/bird -p -c $OUT/bird.conf";
    examples = ./examples;
    user = "bird";
    group = "bird";
    mode = "0640";
  };
  # bird reads its config again on birdc configure, which keeps the sessions a restart would drop.
  chalkos.effects.bird = lib.chalkos.reloadOn { outputs = [ "bird-conf" ]; } [ "bird.service" ];

  users.users.bird = {
    isSystemUser = true;
    group = "bird";
  };
  users.groups.bird = { };
  systemd.services.bird = {
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      ExecStart = "${pkgs.bird2}/bin/bird -f -u bird -g bird -s ${socket} -c ${config.chalkos.outputs.bird-conf.dir}/bird.conf";
      ExecReload = "${pkgs.bird2}/bin/birdc -s ${socket} configure";
      RuntimeDirectory = "bird";
    };
  };
}
```

`lib.chalkos.reloadOn { outputs = [ "bird-conf" ]; } [ "bird.service" ]` is the effect
`{ outputs = [ "bird-conf" ]; kinds = [ ]; reload = [ "bird.service" ]; }`: once a delivery is
applied, chalkd reloads `bird.service` when the files of `bird-conf` changed, and
`lib.chalkos.restartOn` restarts units the same way. An effect's `kinds` fires on a kind's
documents instead, for a service that reads them itself when it starts. Name units by their full
names, as `bird.service`; the image build fails on an effect whose unit the image does not define,
and the evaluation on an `outputs` that names no output.

A change of a node's neighbours needs no new image:
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) delivers the new `Peer`
documents, chalkd renders and checks `bird.conf` before it changes anything, swaps the new file
in, and reloads bird. A `Peer` that gives bird a config `bird -p` rejects refuses the whole
delivery:

```text
output bird-conf: its check refused the rendered files (exit 1); see the node's journal
```

What `bird -p` printed is in the journal of the transient unit the check ran as, which the
journal of `chalkd.service` names, and [`chalkctl logs`](../reference/cli/chalkctl_logs.md)
shows both. The message itself leaves it out, as a program may echo the documents it read.

## Read documents and secrets at runtime

A unit that reads documents while it runs, or a secret, is granted their kinds. A grant,
[`systemd.services.<unit>.chalkos.kinds`](../reference/options.md#systemdserviceschalkoskinds),
makes [chalkd](../reference/glossary.md#chalkd) write the documents of the kinds to
`/run/chalkos/config/<group>/<Kind>/<name>.json`, put
[chalkcfg](../reference/glossary.md#chalkcfg) on the unit's `PATH`, and load the values the
documents' `secretRef` fields name as the unit's credentials. A grant restarts nothing; an effect
does. The node's `Node` document gives its type, for example:

```nix
{ config, pkgs, ... }:
{
  systemd.services.node-type = {
    wantedBy = [ "multi-user.target" ];
    chalkos.kinds = [ "Node" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = pkgs.writeShellScript "node-type" ''
        echo "this node is a $(${config.chalkos.kinds.Node.get { path = "spec.type"; }})"
      '';
    };
  };
}
```

[`chalkos.kinds.<Kind>.get`](../reference/options.md#chalkoskindsget) returns a chalkcfg command,
here `/nix/store/…/bin/chalkcfg get Node -o 'jsonpath={.spec.type}'`, which prints the field for
each document of the kind, one per line. `name` picks one document, and `output` asks for `json`,
`yaml`, `jsonpath=<expression>` or `go-template=<template>` in place of a `path`. A path follows
the kind's options: fields separated by dots, `*` or an index for the elements of a list, and a
key or `*` for the values of an attribute set. Evaluating the image fails on a path the kind
does not have, and the message lists the fields at that point:

```text
chalkos.kinds.Node.get: the path "spec.typ" does not follow the kind's options: spec has no field typ; its fields are labels, nodeIP, taints, type
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
[`chalkcfg get`](../reference/cli/chalkcfg_get.md) lists the exit statuses. networkd's `Network`,
`NetDev` and `Link` have no `get`, because their schemas come from systemd's tables at build time
and a path through them cannot be checked when the image is evaluated; a granted unit runs
`chalkcfg get Network -o json` directly.

A `secretRef` field of a granted kind names a key of a `Secret` document; the unit reads its value
as the systemd credential `secret_<secret>.<key>`, in `$CREDENTIALS_DIRECTORY` or `%d`. chalkcfg
never prints `Secret`s, and their values never appear below `/run/chalkos/config` or in an
output, so a rendered config that needs one names the credential's path. Evaluating the image fails
when a unit is granted a kind the image does not declare.

## Add a rule and a fact to a kind

A rule decides which changes of your kind's documents a node accepts, and a fact gives it what the
node knows. A rule over all of a kind's documents goes into
[`chalkos.kinds.<name>.rules`](../reference/options.md#chalkoskindsrules), where `documents` maps
the names of the delivered documents to them, here allowing two neighbours at most:

```nix
chalkos.kinds.Peer.rules = [
  {
    rule = "size(documents) <= 2";
    message = "a node has two BGP neighbours at most";
  }
];
```

A rule on one field goes into the `rules` of the field's `lib.chalkos.mkOption`, which takes the
same rules as a kind's `rules`. The homelab's `Rack` kind, in
[`extensions/rack.nix`](https://github.com/trevex/chalkos/blob/main/examples/homelab/extensions/rack.nix),
refuses a rack location that differs from the one a rack survey wrote to the node, which
[`chalkos.facts`](../reference/options.md#chalkosfacts) declares as the fact `rackSurvey`:

```nix
chalkos.kinds.Rack.spec.location = lib.chalkos.mkOption {
  type = lib.types.nonEmptyStr;
  example = "rack-a/u12";
  description = "Rack and height unit the node is mounted in.";
  rules = [
    {
      rule = "facts.rackSurvey == null || self == facts.rackSurvey.location";
      messageExpression = ''"the survey found the node at " + facts.rackSurvey.location + ", not at " + self + "; fix the location, or survey the node again"'';
    }
  ];
};
chalkos.facts.rackSurvey.file = "/var/lib/rack-survey/location.json";
```

The rule is CEL: `self` is the delivered location and `facts.rackSurvey` the file's JSON, null
while the file is missing. The rule does not read `oldSelf`, the location the node runs with, so it
checks every `Rack` a node is delivered, its first one too. An install reads every fact as null,
so it takes any location. A fact can also be a command of the image's store, which reads the
config as JSON on standard input and prints the fact as JSON. The image build compiles every rule
and fails on one that does not compile or reads a fact no module declares.

Say the survey file on `cp1` holds `{"location": "rack-b/u3"}`, because the node was moved to
another rack, while `nodes/cp1.yaml` still says `rack-a/u12`. `chalkctl apply-config cp1` then
refuses the config:

```text
the config is refused: Rack "rack" spec.location: the survey found the node at rack-b/u3, not at rack-a/u12; fix the location, or survey the node again
```

The survey and the document disagree, so the node refuses its config until they agree: set
`location: rack-b/u3` in `nodes/cp1.yaml` where the survey is right, or survey the node again
where the document is.

`chalkctl validate` runs the rules that read no facts, such as the `Peer` rule; the rack rule
runs on the node. [Change rules and facts](../concepts/rules.md) explains the variables, the
functions and where rules run.

## Act on a change that no output renders

An effect's `when` fires it on a CEL condition, for a trigger that no output renders, here when
the node gets a new Kubernetes share:

```nix
chalkos.effects.cert-agent = {
  when = "facts.share != oldFacts.share";
  restart = [ "cert-agent.service" ];
};
```

A rule reads the config before and after the delivery as `oldConfig` and `config`, the node's
name as `node.name`, and the facts before and after it as `oldFacts.<name>` and `facts.<name>`.
Only images with Kubernetes declare the `share` fact, so on an image without Kubernetes the build
refuses this rule as one that reads an undeclared fact. Besides `restart` and `reload`, an effect
takes `run`, a command such as `chronyc reload sources`, and `start`, which starts the units of
`restart` that do not run. [Outputs and effects](../concepts/outputs-and-effects.md#what-is-an-effect)
lists the options and the order chalkd takes effects in.

## Know what an image may not change

The image is an appliance: the root file system is a tmpfs, `/etc` is read-only, users are fixed
at build time, and there is no Nix and no `nixos-rebuild` on the node. A service keeps its data
below `/var`, which is [VAR](../reference/glossary.md#var), or on a
[volume](../reference/glossary.md#volume). Within that, a module can set anything, and a few
settings break the node:

- chalkd, `chalkos-config.service`, `chalkos-health.service` and systemd-networkd are what
  install, configure and upgrade the node. An image that disables or replaces them builds, and
  the node then cannot be installed or upgraded.
- The outputs `networkd` and `chrony` render the node's network and time servers, and own
  `/run/systemd/network` and `/run/chalkos/outputs/chrony`, so your outputs take other names and
  directories; the image's evaluation refuses a directory that two outputs share.
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

Build each image, sign a copy of it with [`chalkbuild sign`](../reference/cli/chalkbuild_sign.md)
where the inventory points, then check every node's config against the schemas of its image. The
output directory of `chalkbuild sign` must not exist, so remove the copy of an earlier build first:

```sh
nix build .#node-metal -o build/node-metal
nix build .#gpu-metal -o build/gpu-metal
rm -rf images/node-metal images/gpu-metal
chalkbuild sign --key=db.key --cert=db.crt build/node-metal images/node-metal
chalkbuild sign --key=db.key --cert=db.crt build/gpu-metal images/gpu-metal
chalkctl validate
```

[`chalkctl validate`](../reference/cli/chalkctl_validate.md) reaches no node: it reads each
node's files from the inventory and the schemas from the directory of the node's image, so a
`Rack` document on a node whose image lacks the kind, or a misspelled field, fails here.

## Roll the change out

Upgrade the nodes with [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md), which gives
each node the signed image the inventory names for it, `w1` the new `gpu-metal`:

```sh
chalkctl upgrade
```

Nodes already running their image are skipped. Each node gets its config with the image, the
changed YAML with the new kind's documents included, and checks it against the new image before
the image becomes its next boot; the image the node ran keeps its own config, which a fallback
boots with. A node may move to an image of another variant the same way.
[Upgrade a cluster](upgrade-cluster.md) covers the run, rollbacks and downtime.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) shows the image each node booted,
whether that boot was found healthy, whether it runs the image and the config the inventory gives
it, the hash of each output it serves with its last render or check failure, and names failed
units. The units' journals show what they read:

```sh
chalkctl status cp1
chalkctl logs cp1 --unit=bird.service
chalkctl logs w1 --unit=disk-report.service
```

The status of `cp1` lists the output `bird-conf` as `rendered`, and the journal of
`bird.service` shows it running with the rendered config.

## What next

- [Variants and platforms](../concepts/variants.md) explains how `mkImages` builds variants and
  how an extension adds a kind.
- [Config and the inventory](../concepts/config.md) explains how the YAML reaches the nodes.
- [Outputs and effects](../concepts/outputs-and-effects.md) explains how outputs are rendered,
  checked and swapped in, and when effects fire.
- [Support additional hardware](additional-hardware.md) adds drivers, firmware and platforms.
- [Upgrade a cluster](upgrade-cluster.md) rolls the new images out.
