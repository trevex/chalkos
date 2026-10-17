---
title: "Variants and platforms"
description: "How mkImages builds a cluster's images from variants, platforms and NixOS modules, and how an extension adds a kind"
---

# Variants and platforms

`chalkos.lib.mkImages` builds every [image](../reference/glossary.md#image) of a cluster from NixOS
modules: one image per [variant](../reference/glossary.md#variant) and
[platform](../reference/glossary.md#platform), and one
[installer](../reference/glossary.md#installer). An image fixes the software a node runs and nothing
about the cluster, so one image serves every node of its variant and platform, [control
planes](../reference/glossary.md#control-plane) and [workers](../reference/glossary.md#worker)
alike. What differs between nodes, from their addresses to the cluster's endpoint, is their
[config](../reference/glossary.md#config), YAML documents that [Config and the inventory](config.md)
describes.

## How does a flake build images?

A flake calls `mkImages` and exposes the images it returns as packages. The flake of the
[homelab example](https://github.com/trevex/chalkos/tree/main/examples/homelab) builds one image
for bare metal, with an extension that adds a config kind:

```nix title="flake.nix"
{
  description = "A chalkos home lab cluster";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    let
      # One image for every node, on bare metal; cp1's Node document makes it the control plane.
      # The Rack extension adds the Rack kind and the unit that reads it.
      images = chalkos.lib.mkImages {
        platforms = [ "metal" ];
        variants.node.modules = [ ./extensions/rack.nix ];
      };
    in
    {
      # nix build .#node-metal -o images/node-metal: the image where the Image document in
      # cluster/cluster.yaml points.
      packages.x86_64-linux.node-metal = images.images.node-metal;
    };
}
```

`mkImages` takes these arguments, each an option the
[options reference](../reference/options.md) documents:

| Argument | Default | What it sets |
| --- | --- | --- |
| [`variants`](../reference/options.md#variants) | `{ node = { }; }` | The variants to build, each with its NixOS modules and whether its images run Kubernetes |
| [`platforms`](../reference/options.md#platforms) | `[ "metal" "kvm" ]` | The platforms every variant is built for, as a list of names or with each platform's modules |
| [`modules`](../reference/options.md#modules) | `[ ]` | NixOS modules every variant's images carry |
| [`osCA`](../reference/options.md#osca) | `null` | The public part of the cluster's secrets, whose OS CA the images and the installer trust in maintenance mode |
| [`installer.modules`](../reference/options.md#installermodules) | `[ ]` | The installer's own NixOS modules |
| [`nixpkgs`](../reference/options.md#nixpkgs) | the nixpkgs chalkos is tested against | The nixpkgs the images are evaluated and built from |
| [`system`](../reference/options.md#system) | `x86_64-linux` | The platform the images are built for, as Nix names it |
| [`specialArgs`](../reference/options.md#specialargs) | `{ }` | Extra arguments for every image's modules, such as a flake's inputs |

The arguments are checked as module options, so a misspelled argument fails the evaluation with
its name. `mkImages` returns three attribute sets:

- `images.<variant>-<platform>`, the [image directory](../reference/glossary.md#image-directory)
  of a variant on a platform, such as `images.node-metal`, each built only when asked for;
- `installer`, a raw image and a hybrid ISO in one directory, which installs images of every
  platform;
- `nixos.<variant>-<platform>` and `nixos.installer`, the evaluated NixOS systems, to read an
  image's options, such as `nixos.node-metal.config.chalkos.kubernetes.package.version`.

The flake pins the chalkos version in `flake.lock`, so its images change only when the lock file or
the flake's own modules do. Nix is needed only to build images:
[chalkctl](../reference/glossary.md#chalkctl) and [chalklab](../reference/glossary.md#chalklab)
never evaluate the flake. A node's [`Image`](../reference/kinds.md#image) document names the
directory a build was linked to, so the machine that runs chalkctl needs the directory and no Nix.

## What is a variant?

A variant is a named set of NixOS modules, built for every platform as
`images.<variant>-<platform>`. Its name, of lower-case letters, digits and dashes and starting with
a letter or digit, becomes `CHALKOS_VARIANT` in its images' os-release and `variant` in their
`metadata.json`. A variant declares two things:

- [`modules`](../reference/options.md#variantsmodules), plain NixOS modules for a GPU driver, a
  monitoring agent, more kernel [module groups](../reference/glossary.md#module-group) or an
  [extension](../reference/glossary.md#extension)'s kinds and services;
- [`kubernetes`](../reference/options.md#variantskubernetes), whether its images run
  Kubernetes, `true` by default. One image serves control planes and workers: each node's
  [`Node`](../reference/kinds.md#node) document makes it one or the other.

A cluster needs a second variant only for nodes that run other software, such as GPU nodes that
need drivers the others should not carry. A cluster whose nodes run the same software has one
variant, and its nodes differ only in config. This call builds a `node` variant and a `storage`
variant without Kubernetes, each for `metal` and `kvm`:

```nix
images = chalkos.lib.mkImages {
  osCA = ./secrets.pub.json;
  # Every variant's images carry these.
  modules = [ ./site.nix ];
  variants = {
    node = { };
    storage = {
      kubernetes = false;
      modules = [ ./storage.nix ];
    };
  };
};
```

It returns `images.node-metal`, `images.node-kvm`, `images.storage-metal` and
`images.storage-kvm`. A variant's name with a dash can collide with another's on a platform with
a dash, such as `a-b` on `metal` and `a` on `b-metal`; `mkImages` refuses that pair and names it.

No check compares variants. A node may move to another variant's image with an
[upgrade](../reference/glossary.md#upgrade); the node checks the config the upgrade carries against
the new image's schemas before the image becomes its next boot, so an image that does not accept
the node's config never boots. [Customise an image](../guides/customise-image.md) adds modules to a
variant.

## What is a platform?

A platform is the kind of machine an image runs on, such as `metal` or `kvm`. Its NixOS modules
add what that machine needs and other machines do not: drivers, firmware, agents and the console.
chalkos has modules for two:

- `metal`: the console on the screen and the first serial port, where a BMC's serial-over-LAN
  shows it.
- `kvm`: the console on the first serial port, the virtio disk drivers in the initrd and a QEMU
  guest agent that starts only under KVM. The host may ask the agent about the node and shut it
  down, never run commands or read and write files.

[`platforms`](../reference/options.md#platforms) lists the platforms every variant is built for.
A list names them: `[ "metal" ]` builds metal images alone. An attribute set gives each platform
modules of its own: `{ metal = [ ./firmware.nix ]; dell-r650 = [ ./r650.nix ]; }` adds firmware
to the metal images and builds a `dell-r650` platform from `./r650.nix` alone. Either form
replaces the default, so a platform is built only when it is named.

Building one image per platform keeps each image small, because a universal image would carry
every platform's agents and drivers. A platform's name becomes `CHALKOS_PLATFORM` in its images'
os-release, and a node refuses an image of another platform than the one it runs, so a node
changes platform only by being installed again.
[Support additional hardware](../guides/additional-hardware.md) adds a platform.

## In which order do modules apply?

An image of a variant on a platform is one NixOS system, evaluated from four lists of modules in
this order:

1. chalkos's node modules;
2. the platform's modules: chalkos's own for `metal` and `kvm`, then those `platforms` gives;
3. [`modules`](../reference/options.md#modules), which every variant carries;
4. the variant's own `modules`.

The order decides the order of list entries such as `boot.kernelParams`, and nothing else. A
NixOS value merges by priority, not by module order, so a variant that sets a value its platform
also sets plainly gets a conflict, not an override. It overrides such a value with
`lib.mkForce`; one the platform itself forces, such as the `kvm` platform's `ExecStart` of the
guest agent, it overrides with `lib.mkOverride` and a priority below 50. `lib.mkBefore` and
`lib.mkAfter` place a list's entries.

The installer is a system of its own, built from chalkos's node modules, its installer modules
and [`installer.modules`](../reference/options.md#installermodules). It carries no variant's
modules and no `modules`, because it holds no config for an extension to read, and it belongs to
no platform: one installer writes images of every platform.

## What does an image fix, and what does config set?

An image fixes the software a node runs; config sets every value that names the cluster, the
node or its surroundings. The table lists both sides:

| Fixed when the image is built | Set at runtime by the node's config |
| --- | --- |
| Kernel, kernel modules and initrd | Hostname |
| NixOS services and the variant's modules | systemd-networkd units: `Network`, `NetDev` and `Link` documents |
| Kubernetes release ([`chalkos.kubernetes.package`](../reference/options.md#chalkoskubernetespackage)) and the images of etcd, the pod sandbox and CoreDNS | Whether the node is a control plane or a worker, its labels, taints and addresses |
| The pod network ([`chalkos.cni.provider`](../reference/options.md#chalkoscniprovider)): flannel or none, and the CNI plugins | Cluster name and endpoint, and the cluster's Kubernetes settings: ranges, VIP, extra flags, flannel's MTU, registry mirrors and addons |
| The [OS CA](../reference/glossary.md#os-ca) for maintenance mode, from `osCA` | Storage: system disk, VAR, volumes and encryption |
| Partition sizes of the system region ([`chalkos.disk`](../reference/options.md#chalkosdiskstoresize)) | Time servers and whether DHCP may add more |
| Boot tries and health timeout ([`chalkos.upgrade`](../reference/options.md#chalkosupgradeboottries)) | `Secret` documents and documents of the kinds extensions declare |
| The image's revision and tag, from mkImages's `revision` and `tag`, which only `metadata.json` records | |

A change in the left column is a new image and an [upgrade](upgrades.md). A change in the right
column is delivered with [`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) and
needs no reboot; a storage change that would destroy data is refused there and needs
[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md). Because no cluster value
is in the image, one image serves any number of clusters, and a cluster of three control planes
and fifty workers on one platform builds and signs one image.

## How does an extension add a kind?

An extension is a NixOS module in a variant that declares a config
[kind](../reference/glossary.md#kind) and the services that read its documents. chalkos's own
kinds are declared the same way. The homelab's rack extension records where each node is mounted
and hands the value to a unit on that node:

```nix title="extensions/rack.nix"
# The Rack kind, which records a node's rack position, and a unit that reads it on the node. A
# variant that carries this module accepts Rack documents.
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
    spec.location = lib.mkOption {
      type = lib.types.nonEmptyStr;
      example = "rack-a/u12";
      description = "Rack and height unit the node is mounted in.";
    };
  };
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

The module does three things:

- [`chalkos.kinds.Rack`](../reference/options.md#chalkoskinds) declares the kind: its group,
  version and the options of its `spec`. The image turns the options into a JSON Schema, which
  it carries in `/etc/chalkos/schemas` and its image directory in `schemas/`, so chalkctl and
  chalkd accept `Rack` documents and refuse a misspelled field. A kind's group is a DNS name
  outside `chalkos.dev`, which chalkos keeps for its own kinds, and only option types the schema
  converter knows are allowed; another type fails the build, naming the option.
- [`chalkos.consumers`](../reference/options.md#chalkosconsumers) names the unit that reads the
  kind, so chalkd restarts it when a `Rack` document changes, unless the consumer's
  [`restart.when`](../reference/options.md#chalkosconsumersrestartwhen) rule says otherwise.
- [`chalkos.kinds.<Kind>.get`](../reference/options.md#chalkoskindsget) builds the
  [chalkcfg](../reference/glossary.md#chalkcfg) command that prints the value. The evaluation
  checks the path `spec.location` against the kind's options, so a typo fails the build instead of
  printing nothing on the node.

Each node's value is a `Rack` document in its YAML files; [Config and the
inventory](config.md#what-is-a-config-document) shows one, and
[How do secrets reach a service?](config.md#how-do-secrets-reach-a-service) covers fields that
reference a `Secret`. Only images of a variant that carries the module accept `Rack` documents,
so a node of another variant refuses a config that holds one.

An extension's own image options live under a namespace of its own, such as `rack.*` or
`chalkos.rack.*`. Under `chalkos`, the names chalkos declares are taken: `cni`, `consumers`,
`debug`, `disk`, `kernel`, `kinds`, `kubernetes`, `maintenance`, `platform`, `upgrade` and
`variant`.

## Where does the OS CA come from?

[`chalkctl gen secrets`](../reference/cli/chalkctl_gen_secrets.md) writes the cluster's
[secrets file](../reference/glossary.md#secrets-file) and its public part, `secrets.pub.json`.
[`osCA`](../reference/options.md#osca) names the public part, which holds certificates and no
keys, so it belongs in the repository beside `flake.nix`. Every image and the installer carry its
[OS CA](../reference/glossary.md#os-ca), so chalkd in
[maintenance mode](../reference/glossary.md#maintenance-mode) accepts only clients with a
certificate from it. chalkos reads version 3 of the file only; the evaluation fails on another
version and says to generate new secrets.

Left `null`, images accept any client until they are installed, which suits a lab on a private
network and nothing else. The OS CA is the only value of a cluster an image carries, so images
built with one serve that cluster alone; images built without one serve any. No module reads the
secrets file itself: chalkctl reads it where `--secrets` names it, and the lab template's
`.gitignore` keeps the plaintext `secrets.json` out of git, and so out of the Nix store, which
copies a flake's tracked files.

## Limits

- One image serves every node of its variant and platform, so a per-node kernel parameter, package
  or driver needs a variant or a platform of its own.
- `mkImages` builds images for one Nix `system` per call; a cluster of x86-64 and arm64 machines
  calls it once for each.
- A flake rebuilds an image whenever its modules or `flake.lock` change. A build whose store
  changed has a new [root hash](../reference/glossary.md#root-hash) and is a new image to
  `chalkctl upgrade`; one whose store did not is the image the nodes run already.

## Related pages

- [Config and the inventory](config.md) for the YAML documents nodes receive.
- [The image](image.md) for what an image directory and a disk image hold.
- [Customise an image](../guides/customise-image.md) and
  [Support additional hardware](../guides/additional-hardware.md) for the tasks.
- [mkImages and image options](../reference/options.md) for every argument and option.
