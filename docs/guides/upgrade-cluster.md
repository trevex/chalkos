---
title: "Upgrade a cluster"
description: "Roll a new chalkos or Kubernetes version through a cluster, node by node"
---

# Upgrade a cluster

An [upgrade](../reference/glossary.md#upgrade) installs a new
[image](../reference/glossary.md#image) on every node and reboots the node into it. Any change to
what the image holds goes this way: a new chalkos release, a new Kubernetes release, a change to a
[variant](../reference/glossary.md#variant)'s NixOS modules. [`chalkctl
upgrade`](../reference/cli/chalkctl_upgrade.md) does it for the whole cluster: it writes each node's
inactive [slot](../reference/glossary.md#slot), drains the node, reboots it and waits until the new
image is found healthy, [control planes](../reference/glossary.md#control-plane) one at a time and
the other nodes in batches. A node whose new image never becomes healthy boots its previous image
again, and the run stops there.

Each node reboots once. Workloads move off each node while it reboots, within their
PodDisruptionBudgets.

## Before you begin

- A running cluster, its inventory and the flake that builds its images, with the
  [secrets file](../reference/glossary.md#secrets-file)
  or an operator [client file](../reference/glossary.md#client-file).
- The [Secure Boot](../reference/glossary.md#secure-boot) [db](../reference/glossary.md#db-and-dbx)
  key and certificate, when your nodes enforce Secure Boot and you sign the images during the
  upgrade. [Sign images for Secure Boot](secure-boot-signing.md) explains the keys.
- Three control planes, for an upgrade without API downtime. A cluster with one or two needs
  `--allow-downtime`, described below.
- [Upgrades](../concepts/upgrades.md) explains the mechanism this guide drives.

## Change the image and its version

Every image carries a version, `system.image.version`, which names its
[UKI](../reference/glossary.md#uki) and its slot's partitions, within the rules
[The image](../concepts/image.md#what-may-an-image-version-be) gives. Set it in the NixOS
modules of every variant whose image changes, in the call of
[`chalkos.lib.mkImages`](../reference/options.md) in `flake.nix`. In the homelab example:

```nix title="flake.nix"
{
  description = "A chalkos home lab cluster";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    let
      images = chalkos.lib.mkImages {
        platforms = [ "metal" ];
        variants.node.modules = [
          ./extensions/rack.nix
          { system.image.version = "1.5.0"; }
        ];
      };
    in
    {
      packages.x86_64-linux.node-metal = images.images.node-metal;
    };
}
```

A version set in `modules`, beside `variants`, applies to every variant's images.

Raise the version whenever the image changes, even when you only changed a module. A node refuses an
image of the version it runs with a different store, because the version names its partitions and
boot entry, and chalkctl stops before sending it: `w1 runs another build of 1.4.0, whose store has
the root hash ..., not ...; build the image with a new version`.

Then make the change itself:

- A new chalkos release: update the flake's input and commit the new `flake.lock`.

    ```sh
    nix flake update chalkos
    ```

- A new Kubernetes release: set
  [`chalkos.kubernetes.package`](../reference/options.md#chalkoskubernetespackage) in the
  variant's modules. Every image with Kubernetes carries the kubelet, so every such variant gets
  a new version. Move one minor release at a time, as the Kubernetes version skew policy
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
[Apply config that needs the new image](#apply-config-that-needs-the-new-image) says how. A node
that runs another platform than its `Image` stops the run before any node is sent anything. chalkctl
compares no variant: a node whose `Image` names another variant's build gets that image, and
chalkctl prints the change.

With `--image`, it installs an image you built before, a raw image with `repart-output.json`
next to it or an image directory, on the nodes that run that image's variant, or on the nodes
`--nodes` names. Nodes on another platform are skipped and named.

```sh
chalkctl upgrade --image=images/node-metal
```

On nodes that enforce Secure Boot, each image's UKI must be signed by a key in the firmware's db,
and [chalkd](../reference/glossary.md#chalkd) refuses one that is not before the node can boot it.
`--sign-key` and `--sign-cert` make chalkctl sign the UKIs it sends; an upgrade leaves the boot
loader as it is. An image you sign beforehand with [`chalkctl
sign`](../reference/cli/chalkctl_sign.md) needs no `--sign-key`. `--sign-cert` alone makes chalkctl
check every image against the certificate before sending anything.

## Run the upgrade

Run the command in the inventory's directory, or name the inventory with `--inventory`. It
authenticates with the secrets file `--secrets` names, or with a client file:

```console
$ chalkctl upgrade --secrets=secrets.age --sign-key=<db-key> --sign-cert=<db-cert>
upgrading node on metal to chalkos 1.5.0: cp1, cp2, cp3, w1, w2, w3
cp1: installing 1.5.0
cp1: installed 1.5.0, which boots next as chalkos_1.5.0+3.efi
cp1: cordoned, evicted 2 pods, kept 6
cp1: rebooting into 1.5.0
cp1: runs 1.5.0, found healthy
cp1: uncordoned
...
w1: cordoned, evicted 14 pods, kept 3
w1: installing 1.5.0
w1: installed 1.5.0, which boots next as chalkos_1.5.0+3.efi
w1: rebooting into 1.5.0
w1: runs 1.5.0, found healthy
w1: uncordoned
...
upgraded 6 nodes to 1.5.0
```

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

Raise `--max-unavailable` when the workloads have spare capacity: each batch takes its nodes'
capacity away for one reboot.

## Apply config that needs the new image

An upgrade sends images, never config. With an inventory, chalkctl checks each node's files against
the image the node gets before it drains any node, and stops the run with `the config of <node>:
...` when the image does not accept them, such as a document of a kind its variant no longer
declares. The node checks the config it stores against the new image's schemas too, read from the
slot it has just written, before the image becomes its next boot. A stored config the files no
longer match, such as one whose removed documents were not delivered yet, stops the upgrade of that
node with `the node's config does not suit the image: ...; apply config both images accept with
chalkctl apply-config, then upgrade`. Remove such documents from the node's files and deliver the
config before the upgrade:

```sh
chalkctl apply-config <node> --secrets=secrets.age
```

Config that only the new image accepts, such as a document of a kind an extension of the new
image declares, waits until the node runs that image: the running image would refuse it. Add it
to the node's files after the upgrade, check it and deliver it:

```sh
chalkctl validate <node>
chalkctl apply-config <node> --secrets=secrets.age
```

`<node>` is the node's name in the inventory. Until then
[`chalkctl status`](../reference/cli/chalkctl_status.md) reports that the node runs another config
than the inventory gives it.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) names the version a node booted and
whether that boot was found healthy:

```console
$ chalkctl status w1
...
image 1.5.0, booted from chalkos_1.5.0.efi
...
```

A boot that was not found healthy yet says so: `image 1.5.0, booted from chalkos_1.5.0+2-1.efi, not
found healthy yet`. A node that received its image with `--no-reboot` shows `upgrade to 1.5.0
installed; it boots next`. Then check the cluster from Kubernetes:

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
chalkctl: w2: upgrade to 1.5.0 failed: rolled back to 1.4.0; it stays cordoned; its boots logged:
  ...
  2026-10-10T14:09:41+00:00 w2 chalkd[702]: the node did not become healthy within 5m0s: the kubelet is not healthy: ...
```

The node runs its previous image, healthy, and stays cordoned so you can look at it.
`chalkctl status w2` shows the same lines under `upgrade to 1.5.0 failed: rolled back to 1.4.0`
for as long as the failed image stays on the disk. With them:

1. Find the cause. The journal lines name what the health check found missing: a failed unit on a
   node without Kubernetes, the kubelet, the Node's registration, the API server or the [etcd
   member](../reference/glossary.md#etcd-member). [Troubleshooting](troubleshooting.md) helps from
   there.
2. Fix the image's modules, raise the version, build the image again and run `chalkctl upgrade`
   again. chalkctl refuses to
   install 1.5.0 again on a node that fell back from it, so a known-bad image never boots twice
   by accident.
3. When the cause was outside the image, a switch port that was down for example, install the
   same image once more with `--retry-failed`.

The next run that upgrades the node uncordons it. To give the node back to the scheduler before
that, uncordon it yourself:

```sh
kubectl uncordon w2
```

## Re-run an interrupted upgrade

Run the same command again. chalkctl skips nodes that run the image and were found healthy,
waits for a node that booted it already, does not send an image a node holds already, and
uncordons only nodes it cordoned itself. An image whose transfer was interrupted leaves the
inactive slot empty and the node booting its current image, and the next run writes the slot in
full.

## Go back to the previous version

chalkctl installs older versions as it installs newer ones, and the node prefers the boot entry
it installed last over the newest version, so going back is an upgrade to the previous image.
Use the same image the node ran before, built from the same commit: the node keeps that image's
UKI until its next upgrade, and refuses a different build of the same version while it does.
Keep the previous commit, or the image directory, until the new version has proven itself.

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
- `the boot of chalkos_1.5.0+2-1.efi has not been found healthy yet; upgrade once it is`: the node
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
