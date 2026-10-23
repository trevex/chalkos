---
title: "Add and remove nodes"
description: "Add a node to the inventory and install it, reinstall a node, or take it out of the cluster for good"
---

# Add and remove nodes

This page is for an operator who changes which machines belong to a running cluster. It adds a
worker `w2` to the homelab, reinstalls a node on its machine, and removes `w1`. At the end the
inventory names exactly the nodes that run, and `chalkctl validate` accepts it.

## Before you begin

You need a workstation set up as [Set up your workstation](workstation.md) describes, with the
secrets file. Installing and reinstalling a node need it.

You need to know the new machine's disks and network interfaces, as for any node.
[Write a node's config](node-config.md) describes the documents a node's file holds.

## Add a node to the inventory

A new node needs an entry in `spec.nodes` and a file of its own. Its name, the key in
`spec.nodes`, stays the node's name until it is reinstalled. Its recovery key derives from that
name. The homelab gains a worker `w2`, whose file `nodes/w2.yaml` holds its system disk and
network:

```yaml title="nodes/w2.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: worker
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec:
  disk: {path: /dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100003}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-uplink}
spec:
  match: {name: enp1s0}
  network: {address: [10.0.0.22/24], gateway: 10.0.0.1}
```

The inventory names `w2` with its address and file. `spec.all` gives it the image and the files
of `cluster/`, as it does every node:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    node-metal: {source: {directory: images/node-metal}}
  all: {imageName: node-metal, files: [cluster/*.yaml]}
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    w1: {address: 10.0.0.21, files: [nodes/w1.yaml]}
    w2: {address: 10.0.0.22, files: [nodes/w2.yaml]}
```

Check the new node's config against its image:

```console
$ chalkctl validate w2
w2: valid for the image node-metal (node on metal, root hash d871203debfe), config a4bca06aaaed7a77a58ee95c8d576c74331c47feb48ced26acd9c97b15ee0615
```

Adding a node changes no other node's config, so the other nodes need no `apply-config`.

On the shipped variant, the `Node` document sets the node's type. `type: worker` makes `w2` a
worker. A node with `type: controlplane` joins etcd and the control plane. Grow the control planes
to an odd number, as [Add a control plane](../kubernetes/ha-control-plane.md#add-a-control-plane)
explains.

## Install the new node

Install `w2` as [Install a node](install.md) describes: boot it into maintenance mode, note the
address and the fingerprint, and run `chalkctl install`:

```sh
chalkctl install w2 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
```

`<address>` and `<fingerprint>` are the ones the console shows.

!!! note "On the shipped variant, the new node joins on its own"

    The cluster runs already, so the new node needs no bootstrap. A worker registers with the
    cluster endpoint with the kubelet certificate chalkctl gave it. A control plane joins etcd
    and the control plane.

## Reinstall a node on its machine

A reinstall gives a node a new disk layout, new keys sealed to its TPM and a fresh VAR, from the
node's config in the inventory's files. Everything on its disks is lost. chalkctl has no command
that wipes or resets a whole node. An installed node runs no maintenance mode, so chalkctl
cannot reach it for an install until it boots the installer again.

!!! note "On the shipped variant, take the node out of Kubernetes before step 1"

    Drain a worker with `kubectl drain w1`, and uncordon it with `kubectl uncordon w1` once it is
    `Ready` again. Its kubelet registers its Node again. Take a control plane out of etcd with
    `chalkctl etcd leave` before the reinstall, so that the others keep a correct membership. It
    joins etcd as a new member.
    [Reinstall a control plane](../kubernetes/ha-control-plane.md#reinstall-a-control-plane)
    shows the commands.

To reinstall `w1`:

1. Boot the machine into the installer, through its BMC or its boot menu.
2. Install the node, and let the installer replace what the disk holds:

    ```sh
    chalkctl install w1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint> \
      --wipe-disk
    ```

The node keeps its name and its recovery key, and gets new certificates.
[Replace what a disk holds](install.md#replace-what-a-disk-holds) explains `--wipe-disk`.

A VM installed in place can boot the installer as a CD-ROM in the same way.

[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) wipes and recreates one
volume, not the node. [Reset a volume](storage-volumes.md#reset-a-volume) covers it.

## Remove a node from the cluster

Removing a node takes it out of the inventory and the repository.

!!! note "On the shipped variant, remove the node from Kubernetes before step 1"

    Drain a worker with `kubectl drain w1`. Take a control plane out of etcd with
    `chalkctl etcd leave`, which refuses when the remaining voters would lose their quorum. Then
    delete the Node with `kubectl delete node w1`.
    [Remove a control plane](../kubernetes/ha-control-plane.md#remove-a-control-plane) covers the
    control plane. A control plane whose machine is gone leaves through the others, as
    [Replace a failed machine](../kubernetes/ha-control-plane.md#replace-a-failed-machine)
    describes.

To remove `w1`:

1. Power the machine off.
2. Delete its entry from `spec.nodes` in `chalkos.yaml`.
3. Delete its file, `nodes/w1.yaml`, unless another node still names it.

chalkctl then no longer knows the node:

```console
$ chalkctl validate w1
chalkctl: usage: the inventory chalkos.yaml has no node w1; its nodes are cp1, w2
```

Removing a node from the inventory changes nothing on the machine. Its disk still holds the
installed node, with its encrypted volumes sealed to the machine's TPM. The node's recovery key
still derives from the secrets file and the node's name, so whoever holds the secrets file can
still unlock that disk. chalkos has no command that erases a disk. To use the machine for another
node, install that node with `--wipe-disk`.

The removed node's node certificate also stays valid until it expires, a year after it was issued
or last renewed. Nothing revokes it, and a control plane renews any node certificate of its node
CA, so the node renews it again should the machine run and reach a control plane. On the shipped
variant, the node's kubelet client certificate stays valid until it expires too. To cut the node
off, rotate the OS CA, which gives every remaining node a certificate of a new node CA, and on the
shipped variant the Kubernetes CA.
[Choose what to rotate](access.md#choose-what-to-rotate) describes both rotations.

## Check that it worked

`chalkctl validate` lists exactly the nodes of the inventory. After `w2` is added and `w1` is
removed, it lists `cp1` and `w2`:

```console
$ chalkctl validate
cp1: valid for the image node-metal (node on metal, root hash d871203debfe), config 675850c48eaffe4a755dca41652dbf65a5b7959f1e70a3cdd2a94799136fd200
w2: valid for the image node-metal (node on metal, root hash d871203debfe), config a4bca06aaaed7a77a58ee95c8d576c74331c47feb48ced26acd9c97b15ee0615
```

`chalkctl status w2` shows the new node's config, image and volumes, as
[Install a node](install.md#check-that-it-worked) describes.

## What next

- [Install a node](install.md) covers maintenance mode, the system disk and `--wipe-disk`.
- [Recover a node](recover-node.md) helps a node that no longer unlocks, boots or rejoins.
- [Run a highly available control plane](../kubernetes/ha-control-plane.md) adds and removes
  control planes on the shipped variant.
