---
title: "Upgrade nodes"
description: "Roll a new image through the nodes with chalkctl upgrade, and act on a node that rolls back"
---

# Upgrade nodes

This page is for an operator who installs a new image on running nodes. At the end every node runs
the new image, and its boot was found healthy. A node whose new image never becomes healthy boots
its previous image again. Any change to what the image holds goes this way: a new chalkos release,
a new Kubernetes release or a change to the image's modules. The page upgrades the three
control planes of the `cluster-metal` template.

In short:

```sh
rm -rf images/node-metal-signed
chalkbuild sign --key keys/db.key --cert keys/db.crt images/node-metal images/node-metal-signed
chalkctl validate
chalkctl upgrade --nodes cp3 --secrets=secrets.age
chalkctl upgrade --secrets=secrets.age
chalkctl status cp3
```

A new chalkos release arrives in one of two ways. With the published images, the release publishes
new ones, and the inventory names the new image's digest. With images of your own, the image author
updates the `chalkos` input in the flake's `flake.lock` and builds the images again. Either way,
take chalkctl from the same release, as
[Set up your workstation](workstation.md#get-chalkctl) describes.

## Before you begin

You need the workstation that [Set up your workstation](workstation.md) describes. The upgrade
needs one of these credentials:

- the secrets file `secrets.age`, or an admin client file, to deliver each node's config with the
  image;
- an operator client file, which upgrades too but delivers no config. Each node then carries its
  own config over.

You also need the new image, in a registry or in an image directory, as
[Name the new image in the inventory](#name-the-new-image-in-the-inventory) describes. For your
own images on nodes that enforce Secure Boot, you need the db key and certificate, `db.key` and
`db.crt`, and chalkbuild, as [Install a node](install.md#before-you-begin) describes.

No node of the run may run a config on trial. An upgrade and `upgrade --rollback` stop before
they change any node while one does. Confirm the config with
`chalkctl config confirm <node>` or roll it back with
`chalkctl config rollback <node>` first, as
[Try a config change](change-config.md#try-a-config-change) describes.

Each node stages the new image in its inactive slot, makes it boot next with a number of tries,
and reboots into it. A boot that passes the health check is blessed, and a node whose tries run
out boots its previous image again. [Upgrades](../how-it-works/node/upgrades.md) and
[Boot, health and rollback](../how-it-works/node/boot-and-rollback.md) explain the mechanism.

## Recover a node whose cluster is unavailable

When chalkd is reachable but Kubernetes or etcd cannot start, run `chalkctl upgrade --force` to
install the repair image over the node API. Name every target explicitly:

```sh
chalkctl upgrade --nodes cp1 --force --secrets secrets.age
```

The run reaches only the selected nodes, in batches of `--max-unavailable` without coordinator
ordering. It skips cluster version checks, the guard against config changes that normally need an
ordered rollout, and the layers' role queries, quorum checks, drains and uncordons. Workloads can
be interrupted and etcd can lose quorum. The node's image and config checks, Secure Boot checks
and protection of its fallback image still apply. chalkctl still waits for the new boot to pass
the node's health checks and stops if it falls back. A node cordoned before recovery stays
cordoned. If the node refuses to overwrite the fallback of an unblessed boot,
`chalkctl upgrade --nodes cp1 --rollback --force --secrets secrets.age` recovers by booting the
previous image first.

`--force` is a client option; the installed chalkd needs no new API. Use it with `--until stage`
or `--until activate` to stop before rebooting. Repeat `upgrade --nodes cp1 --force` to continue
from the staged or activated image.

## Name the new image in the inventory

Without `--image`, `chalkctl upgrade` gives each node its image in the inventory, in one of two
forms:

- A reference in a registry, `source.oci`, such as a published image of a chalkos release or your
  own image pushed as [Publish images to a registry](../build/publish-images.md) describes.
  Replace the digest with the new image's, from the release notes or from `chalkbuild push`:

    ```yaml title="chalkos.yaml"
        node-metal: {source: {oci: "ghcr.io/trevex/chalkos/kubernetes-1.37-metal:<tag>@sha256:<digest>"}}
    ```

    Each node pulls the image itself, so it needs a route to the registry, and a private
    registry's credentials in a `Registry` document, as [Registries and mirrors](registries.md)
    describes. The published images are not signed for Secure Boot, so skip the next section.

- An image directory, `source.directory`, such as the build `images/node-metal` or its signed
  copy. chalkctl sends the image to each node. A site whose nodes reach no registry pulls a new
  image of a registry into a directory on the workstation with
  [`chalkctl image pull`](../reference/cli/chalkctl_image_pull.md):

    ```sh
    chalkctl image pull ghcr.io/trevex/chalkos/kubernetes-1.37-metal:<tag>@sha256:<digest> images/node-metal-new
    ```

    The directory must not exist yet, so give each new image a directory of its own and point
    the image's `directory` source at it.

## Sign the new image and validate it

Your own builds for nodes that enforce Secure Boot need signing. The template's inventory names
the image directory `images/node-metal` for every node. For machines that enforce Secure Boot it
names the signed copies, such as `images/node-metal-signed`. Sign each new build again into its
copy. The output directory must not
exist, so remove the copy of the previous build first:

```console
$ rm -rf images/node-metal-signed
$ chalkbuild sign --key keys/db.key --cert keys/db.crt images/node-metal images/node-metal-signed
signed images/node-metal-signed/chalkos.raw
```

Then check each node's config against the new image, without a node. For an image in a registry,
chalkctl reads the image's header and contract from the registry:

```sh
chalkctl validate
```

`chalkctl upgrade` runs the same check before it changes any node, and stops on a config the new
image does not accept. Config that only the new image accepts, such as a document of a kind an
extension of the new image declares, goes into the node's files before the upgrade. Preview what
it changes with `--diff`, which prints each node's changes as
[`config apply --diff`](change-config.md#preview-the-change) does and changes nothing:

```sh
chalkctl validate cp3
chalkctl upgrade --nodes=cp3 --diff --secrets secrets.age
```

The run also stops before it sends anything when a node runs another platform than its image in the
inventory. Changing a node's platform is a reinstall. A node whose image in the inventory has
another name than the image it runs gets that image, and chalkctl prints the change.

A lab catches a broken image before it reaches the cluster. It runs a kvm image with each metal
image's modules, so it tests the modules, the config and Kubernetes, but not the drivers and
firmware of the cluster's machines. Upgrade a lab that runs the same software first, with copies
that `chalklab sign` signed, as
[Sign images for a lab](../build/sign-images.md#sign-images-for-a-lab) shows.

## Upgrade one node, then all

Run the command in the inventory's directory, or name the inventory with `--inventory` or
`$CHALKOSINVENTORY`. Start with
one node, `--nodes cp3`, and check it. Then run the command without `--nodes` for the rest. This
run of the homelab example upgrades its three Kubernetes nodes, `node1` to `node3`, and its router,
which run two images:

```console
$ chalkctl upgrade --secrets secrets.age
upgrading node-metal on metal to <new>: node1, node2, node3
upgrading router-metal on metal to <new-router>: router
node1: installing <new>
node1: config unchanged
node1: installed <new>, staged as .staged-chalkos_2.efi
node1: cordoned, evicted <n> pods, kept <n>
node1: activated <new>, which boots next as chalkos_2+3.efi
node1: rebooting into <new>
node1: runs <new>, found healthy
node1: uncordoned
node2: installing <new>
...
node3: uncordoned
router: installing <new-router>
router: config unchanged
router: installed <new-router>, staged as .staged-chalkos_2.efi
router: activated <new-router>, which boots next as chalkos_2+3.efi
router: rebooting into <new-router>
router: runs <new-router>, found healthy
upgraded 4 nodes to <new>, <new-router>
```

`<new>` and `<new-router>` stand for the new images' names. chalkctl names an image by the first
12 hex characters of its root hash. When the build names a tag or a git revision, they follow in
parentheses, the revision cut to 12 characters, as in `<root hash> (<tag>, <revision>)`. `<n>`
stands for numbers of pods. `.staged-chalkos_2.efi` is the staged UKI on a node that was installed
with `chalkos_1.efi`, and `chalkos_2+3.efi` the boot entry its activation gives it: generation 2,
with three tries.

The first lines name each image and its platform and the nodes that get it. `...` stands
for the lines of node2 and node3, which repeat node1's. Each node shows its steps:

1. `installing` starts sending the image.
2. The config line says what the node's config changes with the image. `config unchanged` means
   the inventory gives the node the config it runs.
3. `installed ..., staged as ...` names the staged UKI. The image checked the config with its own
   checker, renderers included, before the node staged it.
4. `activated ..., which boots next as ...` names the new boot entry.
5. `rebooting into` reboots the node.
6. `runs ..., found healthy` means the boot was blessed.

The last line counts the nodes upgraded.

The control planes go first, one at a time, and the other nodes after them in batches of
`--max-unavailable`, 1 by default. Raise it when the workloads have spare capacity, because each
batch takes its nodes away for one reboot. The homelab's nodes reach each other and the
Kubernetes API through its router alone, so its reboot pauses that traffic. `--nodes router`
upgrades the router in a run of its own, at a time that suits.
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) lists every flag.

`--image` installs an image directory, or a raw image with `repart-output.json` next to it, in
place of the inventory's. It goes to the nodes that run an image of its name, or to the nodes
`--nodes` names, and skips nodes on another platform. A renamed image matches no node that runs
the image before it, so its first upgrade names the nodes with `--nodes`. chalkctl sends the image
as the directory holds it and checks no signature, so sign it first:

```sh
chalkctl upgrade --image=images/node-metal-signed --secrets secrets.age
```

When the inventory names the image in a registry, by `source.oci`, the node pulls the image
itself. [Publish images to a registry](../build/publish-images.md) covers the upgrade from a
registry, and [Registries and mirrors](registries.md) the credentials and mirrors. When the nodes
cannot reach the registry, [`chalkctl image pull`](../reference/cli/chalkctl_image_pull.md) writes
the image into an image directory, which the inventory's `source.directory` or `--image` names.

!!! note "With the Kubernetes layer, chalkctl also drains each node and guards etcd's quorum"

    The `cordoned` and `uncordoned` lines belong to Kubernetes. chalkctl drains a node once its
    image is staged, and activates the image only after the drain, so a refused drain leaves the
    image staged and the node boots its current image. chalkctl also waits until the node's Node
    is Ready. Before a control plane is drained, chalkctl checks that etcd keeps its quorum without
    it. The template has three control planes, so etcd keeps its quorum while each reboots. A
    cluster of one or two control planes loses its quorum while one reboots, and its run needs
    `--allow-downtime`. The Kubernetes layer also adds `--delete-emptydir-data`.
    [Upgrade Kubernetes](../kubernetes/upgrade-kubernetes.md) covers the drain, the quorum, the
    flags and Kubernetes releases.

An upgrade that stopped continues when you run the same command again. chalkctl skips the nodes
that run the new image, and does not send an image again to a node that holds it with the
inventory's config.
[Running the command again continues](../how-it-works/node/upgrades.md#running-the-command-again-continues)
lists what it does with each node.

## Upgrade step by step

`--until` stops each node after a step. A later command runs the next step, for one node at a time:

```console
$ chalkctl upgrade --nodes cp3 --until stage --secrets secrets.age
upgrading node-metal on metal to <new>: cp3
cp3: installing <new>
cp3: config unchanged
cp3: installed <new>, staged as .staged-chalkos_2.efi
staged <new> on 1 node; chalkctl node activate makes it boot next
$ chalkctl node activate cp3 --secrets secrets.age
cp3: activated <new>, which boots next as chalkos_2+3.efi
cp3: chalkctl node reboot cp3 boots it
$ chalkctl node reboot cp3 --secrets secrets.age
cp3: cordoned, evicted <n> pods, kept <n>
cp3: rebooting into <new>
cp3: runs <new>, found healthy
cp3: uncordoned
```

- `--until stage` stages the image on each node and stops. The node keeps booting its current
  image, also across a reboot or a restart of chalkd.
- `chalkctl node activate <node>` makes the image staged on the node boot next. After
  `chalkctl upgrade --image <dir> --until stage`, pass the same `--image <dir>` to it.
- `--until activate` stages and activates the image on each node, and stops before any drain or
  reboot. Each node boots the image at its next reboot.
- `chalkctl node reboot <node>` drains the node, reboots it into the image it boots next, waits
  until its boot is found healthy and uncordons it, as the upgrade does for each node.
  `--no-drain` reboots the node without the drain and still waits for it.

Between the steps, `chalkctl config apply <node>` delivers config to the running image and to the
staged one, as [Deliver config to a staged image](change-config.md#deliver-config-to-a-staged-image)
describes.

## Check that it worked

`chalkctl status` names the image a node booted, the entry it booted from, and whether that boot
was found healthy:

```console
$ chalkctl status cp3 --secrets secrets.age
...
image <new>, booted from chalkos_2.efi
...
```

A blessed entry has lost its counter, so its name has no `+`. A boot that is still being checked
ends in `not found healthy yet`. A node that got its image with `--until stage` shows
`upgrade to <new> staged; node activate makes it boot next`, and one that got it with
`--until activate` or `chalkctl node activate` shows `upgrade to <new> active; it boots next`.

## Go back to the previous image

After an upgrade, a node's inactive slot still holds the image it ran before, with that image's
boot entry and config, until the next upgrade writes over the slot. `chalkctl status` names it:

```text
previous image <old> (upgrade --rollback boots it)
```

`chalkctl upgrade --rollback` boots that image again in place of installing one:

```console
$ chalkctl upgrade --rollback --nodes cp1,cp2,cp3
rolling back 3 nodes to <old>: cp1, cp2, cp3
cp1: cordoned, evicted <n> pods, kept <n>
cp1: rolling back to <old>
cp1: <old> boots next as chalkos_3+3.efi
cp1: rebooting into <old>
cp1: runs <old>, found healthy
cp1: uncordoned
...
cp3: cordoned, evicted <n> pods, kept <n>
cp3: rolling back to <old>
cp3: <old> boots next as chalkos_3+3.efi
cp3: rebooting into <old>
cp3: runs <old>, found healthy
cp3: uncordoned
rolled back 3 nodes to <old>
```

The node writes no image. It renames the previous image's UKI to the next generation, here
`chalkos_3+3.efi`. chalkctl runs it in the order and batches of an upgrade, and it takes
`--nodes`, `--until activate`, `--max-unavailable` and `--timeout`. The image boots with the config
it last ran, so an operator client file suffices. `--image`, `--diff` and `--retry-failed` do not
combine with `--rollback`. Every node of the run must roll back to the same image, and a second
rollback changes nothing. `chalkctl upgrade` to the inventory's image undoes a rollback.
[A rollback boots the image before again](../how-it-works/node/upgrades.md#a-rollback-boots-the-image-before-again)
explains how the run picks its image and when a node keeps none.

Once an upgrade has written over the slot, the way back is an upgrade to the older image directory.
chalkctl and chalkd do not know older from newer and do not warn. Use the image built from the same
commit and `flake.lock`, and keep its directory until the new image has proven itself. Whether the
older image can read what the newer one wrote is for you to check in either case, such as etcd's
data after a Kubernetes upgrade.

## If it fails

### A node rolled back during the upgrade

A node whose new image is not found healthy within its tries boots its previous image. The run stops
at that node:

```text
chalkctl: cp3: upgrade to <new> failed: rolled back to <old>; it stays cordoned; its boots logged:
  <journal lines>
```

`<old>` is the image the node runs again. `<journal lines>` are the last 30 lines that chalkd and the
health check logged during the failed boot, such as
`the node did not become healthy within 5m0s: ...`. `; it stays cordoned` appears on a node of a
Kubernetes cluster alone. `chalkctl status cp3` shows the same lines for as long as the failed image
stays on the disk.

The node runs its previous image, and that boot is healthy. To go on:

1. Find the cause in the journal lines. They name what the health check found missing, such as a
   failed unit. [Troubleshooting](troubleshooting.md) helps from there.
2. When the image caused it, the image author fixes the modules and builds the image again. The
   fix changes the store, so the new build has a new root hash. Run `chalkctl upgrade` again with
   it.
3. When the cause lay outside the image, such as a switch port that was down, install the same
   image once more with `--retry-failed`.

Without `--retry-failed`, chalkctl refuses to install an image again on a node that fell back from
it, with the same error, so that an image known to fail does not boot a second time by accident.
[Uncordon a node that rolled back](../kubernetes/upgrade-kubernetes.md#uncordon-a-node-that-rolled-back)
gives a cordoned node back to the scheduler.

A volume that the failed image's config added stays on the node, and the previous image mounts it
too. The next `chalkctl config apply` for the previous image must name the volume. Without it, the
node refuses the config with
`an earlier config of this image or another added it, with an upgrade or chalkctl config apply
--staged, and it stays on the node; name it in this config too`. Only a reinstall removes the
volume.

[An upgrade rolled back or stopped](troubleshooting.md#an-upgrade-rolled-back-or-stopped) lists
the other messages that stop an upgrade or a rollback, and their fixes.

## What next

- [Change a node's config](change-config.md) delivers YAML changes without a new image.
- [Upgrade Kubernetes](../kubernetes/upgrade-kubernetes.md) moves a cluster to a new Kubernetes
  release in two steps, the control plane first: `chalkctl config apply` delivers the release's
  images in the `Cluster` document, then `chalkctl upgrade` installs images with the new kubelet.
- [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) lists every flag.
