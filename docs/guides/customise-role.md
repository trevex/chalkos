---
title: "Customise a role"
description: "Add NixOS modules, packages, kernel modules and manifests to a role"
---

# Customise a role

A [role](../reference/glossary.md#role)'s image is built from chalkos's node modules, its
[platform](../reference/glossary.md#platform)'s modules and the role's own NixOS modules, which
you add with [`chalkos.roles.<role>.nixosModules`](../reference/options.md#chalkosrolesnixosmodules).
They can add packages, services, kernel modules and anything else a NixOS module sets, within
the limits an image-based node sets. Every node of the role runs the same image, so a role module
sees the cluster's settings but no single node's; per-node values reach a unit at runtime. A
change reaches running nodes as an [upgrade](../reference/glossary.md#upgrade) to a new
[image version](../reference/glossary.md#image-version).

## Before you begin

You need a [cluster definition](../reference/glossary.md#cluster-definition) in a flake and, to roll the change out, a running cluster with an
operator [client file](../reference/glossary.md#client-file) or the [secrets file](../reference/glossary.md#secrets-file).
[The cluster definition](../concepts/cluster-definition.md) explains how roles, platforms and
nodes fit together, and [The image](../concepts/image.md) what an image holds.

## Add a module to a role

A role module is an ordinary NixOS module. This one runs a service on every [worker](../reference/glossary.md#worker), with the
tools it calls in its own `path`:

```nix title="cluster.nix"
{
  chalkos.roles.worker = {
    kubernetes.kind = "worker";
    nixosModules = [
      (
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
      )
    ];
  };
}
```

Nodes have no logins, so the system path holds only what the image's modules put there. Name
the tools a unit runs in its `path`, as above. `environment.systemPackages` works as on any NixOS
system and is the place for what other programs look up there, such as a mount helper
(`mount.nfs`) that a CSI driver runs.

Modules that several roles share go into a list you reuse, or into a platform when they belong to
a kind of machine; [Support additional hardware](additional-hardware.md#define-a-platform-of-your-own)
defines one. A platform's modules come before the role's, but NixOS merges a value by priority, not
by module order: a role overrides a value its platform sets plainly with `lib.mkForce`, and one the
platform sets with `lib.mkForce` itself, such as the `kvm` platform's `ExecStart` of the guest
agent, with `lib.mkOverride` below 50, for example `lib.mkOverride 40`.

## Read cluster settings in a role module

Every cluster setting is available in a role module, read-only, under the name it has in the
cluster definition: `config.chalkos.cluster.name`, `config.chalkos.cluster.endpoint`,
`config.chalkos.cluster.kubernetes.*` and the namespaces of extensions. Use them where a unit needs
a cluster-wide value:

```nix
{ config, ... }:
{
  environment.etc."disk-report/cluster".text = config.chalkos.cluster.name;
}
```

A role module cannot change a cluster setting; the module system refuses a definition of a
read-only option. Settings belong in the cluster definition, so every image agrees on them.

## Pass per-node values to a unit

A role module cannot read `config.chalkos.nodes`. Evaluating it fails with
`config.chalkos.nodes is not available in a role image: one image serves every node of the role`,
because an image is built once for all nodes of its role and platform. Per-node values travel in
the node's [identity](../reference/glossary.md#identity), which [chalkd](../reference/glossary.md#chalkd) applies at boot and on
[`chalkctl apply-identity`](../reference/cli/chalkctl_apply-identity.md). A unit reads them in one
of two ways:

- The whole identity, without secrets, is at
  [`chalkos.node.file`](../reference/options.md#chalkosnodefile), `/run/chalkos/node.json`.
- [`chalkos.node.consumers`](../reference/options.md#chalkosnodeconsumers) names a unit and the
  identity keys it reads. Each key arrives as a systemd credential of the same name, and chalkd
  restarts the unit when one of its keys changes.

Prefer consumers: the unit gets exactly its values, and a change restarts it. A key is a dotted
path, looked up first among the extension values of the identity, then among its own fields:

```nix
{ pkgs, ... }:
{
  systemd.services.hostname-report = {
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${pkgs.coreutils}/bin/cat %d/hostname";
    };
  };
  chalkos.node.consumers.hostname-report.keys = [ "hostname" ];
}
```

`%d` is the unit's credentials directory. A key the identity lacks gets no credential, so the
unit fails to start instead of reading a stale value. Set
[`restartOnChange`](../reference/options.md#chalkosnodeconsumersrestartonchange) to `false` for
a unit that rereads its credentials itself.

## Add a per-node option with an extension

Values of your own, such as a site name per node, come from an
[extension](../reference/glossary.md#extension): a cluster module that declares options in a
namespace of its own and adds modules to roles. A per-node option is declared on
`chalkos.nodes`, and its value lands in the node's identity under the same path:

```nix title="extensions/site.nix"
{ config, lib, ... }:
{
  options.chalkos.site.enable = lib.mkEnableOption "the site name on every worker";

  options.chalkos.nodes = lib.mkOption {
    type = lib.types.attrsOf (
      lib.types.submodule {
        options.site.name = lib.mkOption {
          type = lib.types.str;
          example = "berlin-1";
          description = "Site the node runs at.";
        };
      }
    );
  };

  config = lib.mkIf config.chalkos.site.enable {
    chalkos.roles.worker.nixosModules = [
      (
        { pkgs, ... }:
        {
          systemd.services.site-name = {
            wantedBy = [ "multi-user.target" ];
            serviceConfig = {
              Type = "oneshot";
              ExecStart = "${pkgs.coreutils}/bin/cat %d/site.name";
            };
          };
          chalkos.node.consumers.site-name.keys = [ "site.name" ];
        }
      )
    ];
  };
}
```

Import the file in the cluster definition, set `chalkos.site.enable = true`, and give each worker
`site.name`. A change of a node's `site.name` needs no new image: `chalkctl apply-identity <node>`
delivers it, and chalkd restarts `site-name.service`. The namespace must not be a name chalkos
declares under `chalkos`, in the cluster definition or inside images;
[The cluster definition](../concepts/cluster-definition.md#how-does-an-extension-add-options) lists
them, explains how extensions work and shows a larger one.

## Add kernel modules and debug tools

A role adds [module groups](../reference/glossary.md#module-group) and single modules with
[`chalkos.kernel.moduleGroups`](../reference/options.md#chalkoskernelmodulegroups) and
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

## Add Kubernetes objects to the cluster

Objects every cluster should hold, such as namespaces, a storage class or an operator's
deployment, go into [`chalkos.cluster.manifests`](../reference/options.md#chalkosclustermanifests),
a cluster setting, not a role:

```nix title="cluster.nix"
{
  chalkos.cluster.manifests = [
    {
      apiVersion = "v1";
      kind = "Namespace";
      metadata.name = "apps";
    }
  ];
}
```

The [control planes](../reference/glossary.md#control-plane) apply them with server-side apply after chalkos's own objects, at bootstrap
and at every boot. They travel inside the images, so new objects arrive with an upgrade of the
control planes. Only control-plane images carry the list, so a change to it gives the roles of
kind `controlplane` a new image and leaves the other roles' images as they are.

## What a role may not change

The image is an appliance: the root file system is a tmpfs, `/etc` is read-only, users are fixed
at build time, and there is no Nix and no `nixos-rebuild` on the node. A service keeps its data
below `/var`, which is [VAR](../reference/glossary.md#var), or on a
[volume](../reference/glossary.md#volume). Within that, a role module can set anything, and a few
settings break the node:

- chalkd, `chalkos-identity.service`, `chalkos-health.service` and systemd-networkd are what
  install, configure and upgrade the node. A role that disables or replaces them builds, and the
  node then cannot be installed or upgraded.
- `chalkos.role.name`, `chalkos.role.kubernetes.kind` and `chalkos.platform.name` are set by the
  role builder from the cluster definition; set the role's `kubernetes.kind` there instead.
- The partition sizes under `chalkos.disk` are laid out when a node is installed. A changed size
  applies only to nodes installed afterwards, and an upgrade whose store does not fit a node's
  [slot](../reference/glossary.md#slot) is refused.
- On a role without Kubernetes, a boot is healthy only when no unit that `multi-user.target` or
  `sysinit.target` pulls in failed, so such a unit of yours that fails makes the next upgrade roll
  back. Jobs that timers and sockets start never count.
  [`chalkos.upgrade.healthIgnoreUnits`](../reference/options.md#chalkosupgradehealthignoreunits)
  lists units whose failure does not count.

## Keep the image within its slot

Every package and module adds to the store, which each upgrade sends to every node. The image
build fails when the store's data takes more than 80% of its slot (3 GiB by default,
[`chalkos.disk.storeSize`](../reference/options.md#chalkosdiskstoresize)), its hash tree more than
80% of its partition, or the [UKIs](../reference/glossary.md#uki) more than 80% of the [ESP](../reference/glossary.md#esp), so the next image always fits beside
the running one. [Support additional hardware](additional-hardware.md#keep-the-image-within-its-slot)
shows the message and what to do.

## Roll the change out

An installed node takes a new image only with a new version, because a version names the UKI and
the slot's partitions, and a node refuses an image whose version it has installed with another
root hash. Set `system.image.version` in the role's modules, within the rules
[The image](../concepts/image.md#what-may-an-image-version-be) gives:

```nix
{
  system.image.version = "1.5.0";
}
```

Then build the images and upgrade the nodes with
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md), signed with the cluster's [db](../reference/glossary.md#db-and-dbx) key:

```sh
chalkctl upgrade --nodes=w1,w2 --sign-key=db.key --sign-cert=db.crt
```

Without `--nodes`, every node of the cluster gets its role's image; nodes already running it are
skipped. [Upgrade a cluster](upgrade-cluster.md) covers the run, rollbacks and downtime.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) shows the version each node booted and
whether that boot was found healthy, and names failed units:

```sh
chalkctl status w1
chalkctl logs w1 --unit=disk-report.service
```

## What next

- [The cluster definition](../concepts/cluster-definition.md) explains roles, platforms, the
  identity and extensions.
- [The image](../concepts/image.md) describes what a role's image holds.
- [Support additional hardware](additional-hardware.md) adds drivers and firmware.
- [Upgrade a cluster](upgrade-cluster.md) rolls the new images out.
