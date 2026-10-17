---
title: "Upgrade a cluster"
description: "Roll a new chalkos or Kubernetes version through a cluster, node by node"
---

# Upgrade a cluster

An [upgrade](../reference/glossary.md#upgrade) installs a new
[image](../reference/glossary.md#image) on every node and reboots the node into it. Any change to
what the image holds goes this way: a new chalkos release, a new Kubernetes release, a change to a
[variant](../reference/glossary.md#variant)'s NixOS modules.
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) does it for the whole cluster: it writes
each node's inactive [slot](../reference/glossary.md#slot), drains the node, reboots it and waits
until the new image is found healthy, [control planes](../reference/glossary.md#control-plane) one
at a time and the other nodes in batches. A node whose new image never becomes healthy boots its
previous image again, and the run stops there.

Each node reboots once. Workloads move off each node while it reboots, within their
PodDisruptionBudgets.

## Before you begin

- A running cluster, its inventory and the flake that builds its images, with the
  [secrets file](../reference/glossary.md#secrets-file)
  or an admin [client file](../reference/glossary.md#client-file). An operator client file upgrades
  too, but delivers no config: each node carries its own over.
- The [Secure Boot](../reference/glossary.md#secure-boot) [db](../reference/glossary.md#db-and-dbx)
  key and certificate, when your nodes enforce Secure Boot and you sign the images during the
  upgrade. [Sign images for Secure Boot](secure-boot-signing.md) explains the keys.
- Three control planes, for an upgrade without API downtime. A cluster with one or two needs
  `--allow-downtime`, described below.
- [Upgrades](../concepts/upgrades.md) explains the mechanism this guide drives.

## Change the image

An image is its store's [root hash](../reference/glossary.md#root-hash), so any change to its
modules or to `flake.lock` makes a new image, and nothing needs raising before it is installed. A
node numbers the boot entries it installs itself, and boots the one installed last. The call of
[`chalkos.lib.mkImages`](../reference/options.md) in `flake.nix` passes the commit the images are
built from as `revision`, which `chalkctl status` and `chalkctl upgrade` show beside the root
hash. In the homelab example:

```nix title="flake.nix"
{
  description = "A chalkos home lab cluster";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { self, chalkos, ... }:
    let
      images = chalkos.lib.mkImages {
        revision = self.rev or self.dirtyRev or null;
        platforms = [ "metal" ];
        variants.node.modules = [ ./extensions/rack.nix ];
      };
    in
    {
      packages.x86_64-linux.node-metal = images.images.node-metal;
    };
}
```

Nix cannot read git tags, so a release job that builds from a tag passes it as `tag` as well, such
as `tag = "v1.5.0";`. Both are for display only. Make the change itself:

- A new chalkos release: update the flake's input and commit the new `flake.lock`.

    ```sh
    nix flake update chalkos
    ```

- A new Kubernetes release: set
  [`chalkos.kubernetes.package`](../reference/options.md#chalkoskubernetespackage) in the
  variant's modules. Every image with Kubernetes carries the kubelet, so every such variant gets
  a new image. Move one minor release at a time, as the Kubernetes version skew policy
  requires; `chalkctl upgrade` upgrades the control planes before the
  [workers](../reference/glossary.md#worker), which keeps the kubelets no newer than the API
  server.

## Decide how the images are built and signed

Without `--image`, `chalkctl upgrade` gives each node the image its `Image` document names, the
[image directory](../reference/glossary.md#image-directory) of a build. Build the images first,
where the `Image` documents point, and check the nodes' config against them with
[`chalkctl validate`](../reference/cli/chalkctl_validate.md). The homelab example's `Image`
names `../images/node-metal`, relative to `cluster/cluster.yaml`:

```sh
nix build .#node-metal -o images/node-metal
chalkctl validate
```

`validate` checks each node's config against the new image's schemas, offline. `chalkctl upgrade`
runs the same check before it drains any node and stops the run on a config the new image does
not accept, so fix it now:
[Upgrade with config the new image needs](#upgrade-with-config-the-new-image-needs) says how. A
node that runs another platform than its `Image` stops the run before any node is sent anything.
chalkctl compares no variant: a node whose `Image` names another variant's build gets that image,
and chalkctl prints the change.

With `--image`, it installs an image you built before, a raw image with `repart-output.json`
next to it or an image directory, on the nodes that run that image's variant, or on the nodes
`--nodes` names. Nodes on another platform are skipped and named.

```sh
chalkctl upgrade --image=images/node-metal
```

On nodes that enforce Secure Boot, each image's UKI must be signed by a key in the firmware's db,
and [chalkd](../reference/glossary.md#chalkd) refuses one that is not before the node can boot it.
`--sign-key` and `--sign-cert` make chalkctl sign the UKIs it sends; an upgrade leaves the boot
loader as it is. An image you sign beforehand with
[`chalkctl sign`](../reference/cli/chalkctl_sign.md) needs no `--sign-key`. `--sign-cert` alone
makes chalkctl check every image against the certificate before sending anything.

## Run the upgrade

Run the command in the inventory's directory, or name the inventory with `--inventory`. It
authenticates with the secrets file `--secrets` names, or with a client file:

```console
$ chalkctl upgrade --secrets=secrets.age --sign-key=<db-key> --sign-cert=<db-cert>
upgrading node on metal to 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6): cp1, cp2, cp3, w1, w2, w3
cp1: config unchanged
cp1: installing 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)
cp1: installed 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6), which boots next as chalkos_2+3.efi
cp1: cordoned, evicted 2 pods, kept 6
cp1: rebooting into 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)
cp1: runs 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6), found healthy
cp1: uncordoned
...
w1: cordoned, evicted 14 pods, kept 3
w1: config unchanged
w1: installing 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)
w1: installed 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6), which boots next as chalkos_2+3.efi
w1: rebooting into 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)
w1: runs 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6), found healthy
w1: uncordoned
...
upgraded 6 nodes to 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)
```

chalkctl names an image by the first 12 hex characters of its root hash, followed by the tag and
the revision's first 12 characters when the build named them. `chalkos_2+3.efi` is the node's
next boot entry: one generation above the entry it booted, with three tries.

`<db-key>` and `<db-cert>` are the PEM files of the Secure Boot db key and its certificate. The
lines follow each node through its steps:

1. Before a control plane is touched, chalkctl checks that etcd keeps its quorum without it:
   the other voters must be healthy and enough for a quorum. After the previous control plane
   came back, it retries this check for up to a minute while etcd settles.
2. chalkctl sends the image. The node writes it into its inactive slot, checks it, and makes it
   the next boot with three tries, the image's
   [`chalkos.upgrade.bootTries`](../reference/options.md#chalkosupgradeboottries). A worker is
   drained before it receives the image, a control plane after.
3. The drain cordons the node and evicts its pods through the eviction API, so
   PodDisruptionBudgets apply. DaemonSet pods, static pods, finished pods and pods without a
   controller are kept; chalkctl names the last kind, because nothing restarts them elsewhere.
4. The node reboots. The new image's [health check](../reference/glossary.md#health-check)
   decides whether the boot is healthy; chalkctl waits until it was found healthy and, on a
   Kubernetes node, its Node is Ready.
5. chalkctl uncordons the node, if it cordoned it.

Control planes go first, one at a time. Workers and nodes without Kubernetes follow, in batches.

### Choose how many nodes go at once

| Flag | Effect |
| --- | --- |
| `--max-unavailable=<n>` | Upgrades `<n>` workers or nodes without Kubernetes at once; 1 by default. Control planes always go one at a time. |
| `--nodes=<nodes>` | Upgrades only the comma-separated nodes named. |
| `--allow-downtime` | Lets a cluster of one or two control planes upgrade, although etcd loses its quorum while one of them reboots. etcd and the API server are down until it is back. With three or more, the flag changes nothing: chalkctl still waits for the other voters to be healthy. |
| `--no-reboot` | Installs the images without draining or rebooting; each node boots its image at its next reboot. |
| `--delete-emptydir-data` | Also evicts pods with `emptyDir` volumes, whose data is lost. Without it, such a pod stops the drain. |
| `--timeout=<duration>` | How long to wait for each drain and for each node to come back healthy; 30 minutes by default. |
| `--diff` | Prints what each node's config changes and changes nothing. |
| `--password-file=<file>` | Holds the password of nodes whose fallback is a password, for a new encrypted volume the config adds. |

Raise `--max-unavailable` when the workloads have spare capacity: each batch takes its nodes'
capacity away for one reboot.

## Upgrade with config the new image needs

An upgrade carries each node's config with its image. With an inventory, chalkctl checks each
node's files against the image the node gets before it drains any node, and stops the run with
`the config of <node>: ...` when the image does not accept them, such as a document of a kind its
variant no longer declares. The node checks the config against the new image's schemas too, read
from the slot it has just written, and keeps it for that image. Config that only the new image
accepts, such as a document of a kind an extension of the new image declares, goes into the
node's files before the upgrade:

```sh
chalkctl validate <node>
chalkctl upgrade --nodes=<node> --diff
```

`<node>` is the node's name in the inventory. `chalkctl validate` checks the files against the
image the node's `Image` names, which is the image the upgrade installs without `--image`.
`--diff` prints, for each node, what its config changes against the config of the image it runs,
and changes nothing:

```text
w1: config 1 added, 0 removed, 0 changed
+ Rack "rack"
```

Without `--diff`, chalkctl prints the first line for each node before it sends the image. The
image the node runs keeps its own config, so a node that falls back runs the config it ran before
the upgrade.

The config counts against the node's storage and Kubernetes settings as `apply-config` does. A
destructive storage change stops the node's upgrade, and a new volume is created before the image
boots. A new encrypted volume needs the fallback secret: the recovery key, which chalkctl derives
from the secrets file and no client file gives, or with a password fallback the password in the
file `--password-file` names. Without `--password-file`, chalkctl asks for each node's password on
the terminal, one node after the other, before it drains the first node.

Delivering config with the upgrade needs the admin role: the secrets file, or an admin client
file. With an operator client file the upgrade still runs, but chalkctl sends no config, prints
`config not delivered: the operator role cannot deliver config; each node carries its own over`,
and each node checks the config of the image it runs against the new image and keeps it for that
image. `--diff` needs the admin role too.

### Change the config of a staged image

After an upgrade with `--no-reboot`, each node keeps the new image's config until it reboots into
the image. Deliver a change to that config with `--staged`:

```sh
chalkctl apply-config <node> --staged --secrets=secrets.age
```

chalkctl checks the files against the image the node's `Image` names and the node checks them
against the staged image; nothing restarts. `chalkctl apply-config` without `--staged` changes the
running image's config alone and warns that the staged image keeps its own. Running
`chalkctl upgrade` again sends the image once more to a node whose staged config is not the
inventory's, which replaces that config.

A change to the running image that adds a volume the staged image's config lacks is refused: once
the volume exists, the staged image's config would count as removing it, and the node could not
write that config again. The staged image would still mount the volume, as every boot sets up the
volumes `storage.json` records. Deliver the change with `--staged` first, which creates the
volume, then without.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) names the image a node booted, the
entry it booted from and whether that boot was found healthy:

```console
$ chalkctl status w1
...
image 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6), booted from chalkos_2.efi
...
```

A boot that was not found healthy yet says so:
`image 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6), booted from chalkos_2+2-1.efi, not found healthy yet`.
A node that received its image with `--no-reboot` shows
`upgrade to 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6) installed; it boots next`. Then check the cluster
from Kubernetes:

```sh
kubectl get nodes -o wide
```

## If a node rolls back

A new image that does not become healthy within
[`chalkos.upgrade.healthTimeout`](../reference/options.md#chalkosupgradehealthtimeout), 300
seconds by default, is rebooted; after its last try, systemd-boot boots the previous image. The
health check wrote the last 30 lines that chalkd and the check logged during the failed boot to
[VAR](../reference/glossary.md#var), and the previous image shows them. The run stops at that node:

```text
chalkctl: w2: upgrade to 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6) failed: rolled back to 81bd02c4a9e1 (v1.4.0, 0f1e2d3c4b5a); it stays cordoned; its boots logged:
  ...
  2026-10-10T14:09:41+00:00 w2 chalkd[702]: the node did not become healthy within 5m0s: the kubelet is not healthy: ...
```

The node runs its previous image, healthy, and stays cordoned so you can look at it.
`chalkctl status w2` shows the same lines under
`upgrade to 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6) failed: rolled back to 81bd02c4a9e1 (v1.4.0, 0f1e2d3c4b5a)`
for as long as the failed image stays on the disk. With them:

1. Find the cause. The journal lines name what the health check found missing: a failed unit on a
   node without Kubernetes, the kubelet, the Node's registration, the API server or the [etcd
   member](../reference/glossary.md#etcd-member). [Troubleshooting](troubleshooting.md) helps from
   there.
2. Fix the image's modules, build the image again and run `chalkctl upgrade` again: the fix
   changes the store, so the new build has a new root hash. chalkctl refuses to install
   3fa9c1e2b7d0 again on a node that fell back from it, so a known-bad image never boots twice
   by accident.
3. When the cause was outside the image, a switch port that was down for example, install the
   same image once more with `--retry-failed`.

The next run that upgrades the node uncordons it. To give the node back to the scheduler before
that, uncordon it yourself:

```sh
kubectl uncordon w2
```

### Keep a volume the new image's config added

A volume that the failed image's config added stays on the node. The upgrade created it before
the image booted, and the previous image mounts it too, since every boot sets up the volumes
`storage.json` records. The previous image's config does not name the volume, so a later
`chalkctl apply-config` to that image must name it as well. A config without it is refused, and
the refusal names the volume and says
`the config of another image added it, with an upgrade or chalkctl apply-config --staged, and it stays on the node; name it in this config too`,
followed by the image whose config still names it, while that image has a slot. The same holds
for a volume that `apply-config --staged` added for an image the node never booted.

## Re-run an interrupted upgrade

Run the same command again. chalkctl skips nodes that run the image and were found healthy,
waits for a node that booted it already, does not send an image a node holds already, and
uncordons only nodes it cordoned itself. An image whose transfer was interrupted leaves the
inactive slot empty and the node booting its current image, and the next run writes the slot in
full.

## Go back to the previous image

Going back is an upgrade to the previous image, at your own risk: chalkctl and chalkd do not
know older from newer and do not warn. The node installs it as the next generation, so it boots
first, with its tries, like any upgrade. Use the image the node ran before, built from the same
commit and `flake.lock`, so it has the same root hash: the node's inactive slot still holds that
store, which is then not sent to the disk again. Keep the previous commit, or the image directory,
until the new image has proven itself. Whether the older image suits what the newer one wrote,
such as etcd's data after a Kubernetes upgrade, is for you to check.

## If something goes wrong

- `without cp2 etcd has 1 healthy voter of 3, fewer than the 2 its quorum needs`: another
  control plane is down or unhealthy. chalkctl does not reboot cp2 until enough of the others are
  healthy; `chalkctl etcd members` shows which one is not.
- `etcd has 1 voter, and without cp1 fewer than the 1 its quorum needs: ...; pass --allow-downtime
  to accept that`: the cluster has one or two control planes, so the API server is down while one
  reboots. Pass `--allow-downtime` when that is acceptable.
- `pods ... have emptyDir volumes, whose data an eviction deletes; pass --delete-emptydir-data to
  evict them`: the drain refuses to delete those pods' scratch data unless you allow it.
- `etcd member cp3 is a learner yet; upgrade once it joined`: a control plane is still joining;
  run again once it finished.
- `the boot of chalkos_2+2-1.efi has not been found healthy yet; upgrade once it is`: the node
  runs an image that is still being checked, and installing another would remove the image it
  falls back to. Wait for the check, at most `healthTimeout` per try.
- `Secure Boot would refuse the UKI`: the image is unsigned or signed with a key the firmware's db
  does not hold. Sign it with the db key.
- `w1 runs an image built for kvm, but its Image node-metal is built for metal; changing a node's
  platform is a reinstall`: the node's `Image` names a build of another platform. Changing a node's
  platform is a reinstall, not an upgrade.
- A drain that does not finish within `--timeout` leaves the node cordoned and stops the run.
  A PodDisruptionBudget that allows no eviction is the usual cause; `kubectl get pdb -A` shows
  it.

## What next

- [Upgrades](../concepts/upgrades.md) and [Boot, health and
  rollback](../concepts/boot-and-rollback.md) explain the slots, [boot
  counting](../reference/glossary.md#boot-counting) and the health check.
- [Troubleshooting](troubleshooting.md) starts from the symptoms of a failed boot.
- [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) lists every flag.
