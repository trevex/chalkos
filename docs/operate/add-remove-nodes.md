---
title: "Add and remove nodes"
description: "Add a node to the inventory and install it, reinstall a node, take it out of the cluster for good, or reuse its name"
---

# Add and remove nodes

This page is for an operator who changes which machines belong to a running cluster. It adds a
worker `w1` to the cluster of the `cluster-metal` template, reinstalls a node on its machine,
removes and revokes `w1` again, and gives a revoked name to a new node. At the end the inventory
names exactly the nodes that run, and `chalkctl validate` accepts it.

## Before you begin

You need a workstation set up as [Set up your workstation](workstation.md) describes, with the
secrets file. Installing and reinstalling a node need it.

You need to know the new machine's disks and network interfaces, as for any node.
[Write a node's config](node-config.md) describes the documents a node's file holds.

## Add a node to the inventory

A new node needs an entry in `spec.nodes` and a file of its own. Its name, the key in
`spec.nodes`, stays the node's name until it is reinstalled. Its recovery key derives from that
name. The cluster gains a worker `w1` on the network of the control planes. Its file
`nodes/w1.yaml` is a copy of `cp1`'s with the new machine's disk, the MAC address of its port,
the address `192.168.1.14` and `type: worker`:

```yaml title="nodes/w1.yaml"
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
metadata: {name: 10-lan}
spec:
  match: {macAddress: "02:00:00:00:00:14"}
  network: {address: [192.168.1.14/24], gateway: 192.168.1.1, dns: [192.168.1.1]}
```

The inventory names `w1` with its address and file. `spec.all` gives it the image and the files
of `cluster/`, as it does the control planes:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: cluster}
spec:
  images:
    node-metal: {source: {directory: images/node-metal}}
  all: {imageName: node-metal, files: [cluster/*.yaml]}
  nodes:
    cp1: {address: 192.168.1.11, files: [nodes/cp1.yaml]}
    cp2: {address: 192.168.1.12, files: [nodes/cp2.yaml]}
    cp3: {address: 192.168.1.13, files: [nodes/cp3.yaml]}
    w1: {address: 192.168.1.14, files: [nodes/w1.yaml]}
```

Check the new node's config against its image:

```console
$ chalkctl validate w1
w1: valid for the image node-metal (node-metal on metal, root hash 98bb85096ed9), config d293e0172dbc6c5bef6eba9da6cd05d1ec9ae19c1a93f604662d1dc5adc2dd41
```

Adding a node changes no other node's config, so the other nodes need no `config apply`.

With the Kubernetes layer, the `Node` document sets the node's type. `type: worker` makes `w1` a
worker. A node with `type: controlplane` joins etcd and the control plane. Grow the control planes
to an odd number, as [Add a control plane](../kubernetes/ha-control-plane.md#add-a-control-plane)
explains.

In a cluster with a `ClusterRegistry`, a new control plane changes the other nodes' config: every
control plane serves the cluster registry, so its address goes into the document's `endpoints`
before you install it, and its config is refused until the list names it. Apply the config to
every node, and after the install push the cluster registry's images again, so the new endpoint
holds them. [Add or remove a control plane](../kubernetes/cluster-registry.md#add-or-remove-a-control-plane)
lists the steps.

## Install the new node

Install `w1` as [Install a node](install.md) describes: boot it into maintenance mode, note the
address and the fingerprint, and run `chalkctl install`:

```sh
chalkctl install w1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
```

`<address>` and `<fingerprint>` are the ones the console shows.

!!! note "With the Kubernetes layer, the new node joins on its own"

    The cluster runs already, so the new node needs no bootstrap. A worker registers with the
    cluster endpoint with the kubelet certificate chalkctl gave it. A control plane joins etcd
    and the control plane.

## Reinstall a node on its machine

A reinstall gives a node a new disk layout, new keys sealed to its TPM and a fresh VAR, from the
node's config in the inventory's files. Everything on its disks is lost. chalkctl has no command
that wipes or resets a whole node. An installed node runs no maintenance mode, so chalkctl
cannot reach it for an install until it boots the installer again.

!!! note "With the Kubernetes layer, take the node out of Kubernetes before step 1"

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

Removing a node takes it out of the inventory and the repository. With the Kubernetes layer, it also
revokes the node, so the machine cannot come back with the certificates it still holds.

!!! note "With the Kubernetes layer, take the node out of Kubernetes before step 1"

    Drain a worker with `kubectl drain w1`. Take a control plane out of etcd with
    `chalkctl etcd leave`, which refuses when the remaining voters would lose their quorum.
    [Remove a control plane](../kubernetes/ha-control-plane.md#remove-a-control-plane) covers the
    control plane. A control plane whose machine is gone leaves through the others, as
    [Replace a failed machine](../kubernetes/ha-control-plane.md#replace-a-failed-machine)
    describes.

To remove `w1`:

1. With the Kubernetes layer, revoke the node with `chalkctl node revoke w1`, as
   [Revoke a removed node](#revoke-a-removed-node) describes.
2. Power the machine off.
3. Delete its entry from `spec.nodes` in `chalkos.yaml`.
4. Delete its file, `nodes/w1.yaml`, unless another node still names it.

chalkctl then no longer knows the node:

```console
$ chalkctl validate w1
chalkctl: usage: the inventory chalkos.yaml has no node w1; its nodes are cp1, cp2, cp3
```

A control plane of a cluster with a `ClusterRegistry` serves an endpoint of the cluster registry.
Once it is out of the inventory, take its address out of the document's `endpoints` and apply the
config to every remaining node. Until then, containerd passes over the missing endpoint to the
next source.

Removing a node from the inventory changes nothing on the machine. Its disk still holds the
installed node, with its encrypted volumes sealed to the machine's TPM. The node's recovery key
still derives from the secrets file and the node's name, so whoever holds the secrets file can
still unlock that disk. chalkos has no command that erases a disk. To use the machine for another
node, install that node with `--wipe-disk`.

The removed node's certificates also stay valid until they expire. Its node certificate expires a
year after it was issued or last renewed. A node whose image runs no Kubernetes renews it only
through `chalkctl node renew`, so not renewing it ends the node's access within a year.
`chalkctl rotate os-ca` ends it at once, because it gives every remaining node a certificate of a
new node CA. [Choose what to rotate](access.md#choose-what-to-rotate) describes the rotation.

## Revoke a removed node

With the Kubernetes layer, a removed node can still renew its node certificate through a control
plane, and its kubelet client certificate stays valid for up to a year. With them, the machine
could register its Node again. `chalkctl node revoke` cuts the node off by name, at once and
without a rotation:

```console
$ chalkctl node revoke w1 --secrets secrets.age
revoked node w1 (since 2026-10-23T22:42:25Z); deleted its Node object
```

A control plane records the name and the time in the ConfigMap `kube-system/chalkos-revoked-nodes`
and deletes the node's Node object, so `kubectl delete node` is not needed. The revocation
takes effect within seconds, and the other control planes' API servers pick it up shortly after.
From then on:

- Control planes refuse to renew the node's node certificate.
- chalkd approves none of its kubelet serving certificates.
- The API server denies every write by its kubelet, so the kubelet can neither register its Node
  again nor renew its kubelet client certificate.
- `chalkctl rotate` skips the node, and `chalkctl node renew`,
  `chalkctl config apply --kubernetes-share` and `chalkctl install` refuse it unless `--force` is
  given.

The command needs an admin client file or the secrets file. It goes through a control plane:
the one `--via` names, or the first other one that answers. The removed node does not have to run,
and it does not have to be in the inventory any more. Revoking a node twice keeps the first time.
Every control plane applies the API server's admission policy again within about 10 seconds of a
change to the ConfigMap, so an edit with `kubectl edit` takes effect too. Revoke and unrevoke
nodes with chalkctl all the same, because an edit by hand skips the commands' checks.

Before it revokes the node, `chalkctl node revoke` asks every other control plane whether it reads
the revoked nodes, and refuses while one does not answer or cannot read the list:

```text
chalkctl: not every control-plane node reads the revoked nodes (cp2: cannot read the revoked nodes: read the revoked nodes: connection refused); repair those, or pass --force to revoke w1 anyway
```

Repair that control plane, then revoke again. `--force` revokes the node at once; the API server
denies its writes all the same.

A control plane also refuses a revocation that would make the list too long for the API server to
compile the admission policy, at about 2,000 short names. Unrevoke names whose CAs were rotated
since, as [Reuse a removed node's name](#reuse-a-removed-nodes-name) describes, to make room.

`chalkctl status` on a control plane lists the revoked nodes:

```text
revoked nodes:
  w1  since 2026-10-23T22:42:25Z
```

The revoked node keeps its certificates until they expire, and the API server still answers its
kubelet's reads. [What revoking a node cuts off](../how-it-works/kubernetes/certificates.md#revoking-a-node-cuts-off-its-writes-and-renewals)
lists what the node can still do.

A control plane cannot be cut off this way, because its disk holds the node CA's and the
Kubernetes CA's keys. `chalkctl node revoke` refuses a node that is still an etcd member, and the
control plane that `--via` names. Take the control plane out of etcd first, with
`chalkctl etcd leave`, or with `chalkctl etcd remove-member` when its machine is gone. Revoke it,
then rotate `os-ca` and `kubernetes-ca` with `chalkctl rotate`, as
[Manage access and certificates](access.md#rotate-the-os-ca) and
[Rotate the Kubernetes CAs and keys](../kubernetes/rotate-kubernetes-cas.md) describe.
`--force` revokes an etcd member all the same.

Revocation needs the Kubernetes layer. A node whose image lacks it answers
`chalkctl node revoke` and `chalkctl node unrevoke` with `UNIMPLEMENTED` and
`the kubernetes layer is not in this image`, so a `--via` that names such a node fails. A cluster
without Kubernetes keeps no revoked nodes. There, a removed node loses its access when nobody
renews its node certificate, within a year, or at once with `chalkctl rotate os-ca`, as
[Remove a node from the cluster](#remove-a-node-from-the-cluster) describes.

## Reuse a removed node's name

A revoked name stays blocked for every machine, so a node installed afresh as `w1` is shut out as
well. To give the name to a new node, first rotate the two CAs the old node's certificates chain
to. Then remove the name from the revoked nodes:

1. Rotate the OS CA with `chalkctl rotate os-ca`, to its finish, as
   [Rotate the OS CA](access.md#rotate-the-os-ca) describes.
2. Rotate the Kubernetes CA with `chalkctl rotate kubernetes-ca`, to its finish, as
   [Rotate the Kubernetes CAs and keys](../kubernetes/rotate-kubernetes-cas.md) describes.
3. Unrevoke the name:

    ```console
    $ chalkctl node unrevoke w1 --secrets secrets.age
    unrevoked node w1; the certificates it held before <time> are no longer trusted
    ```

    `<time>` is when the node was revoked.

4. Add the node to the inventory and install it, as [Install the new node](#install-the-new-node)
   describes. It joins as usual.

Unrevoke a name once the rotations are done, even when no new node takes it. The list holds
about 2,000 short names at most, and after the rotations the old node's certificates no longer
work anyway. Unrevoke before or after a rotation, not while one is paused: a node unrevoked
between two runs of `chalkctl rotate` missed the phases it was skipped in, and the rotation may
stop at it.

The control plane checks that the OS CA and the Kubernetes CA it trusts were all made after the
revocation. While one of them predates it, the old node's certificates would work again, so
`chalkctl node unrevoke` refuses and names the rotations missing:

```text
chalkctl: cp1: failed_precondition: w1 was revoked at 2026-10-23T22:42:25Z, but the OS CA (chalkctl rotate os-ca) and the Kubernetes CA (chalkctl rotate kubernetes-ca) may predate the revocation: the old node's certificates would work again until they expire; rotate them first, or pass --force to unrevoke w1 anyway
```

`--force` unrevokes the node without the rotations. Use it to let the old machine back in, for
example after revoking the wrong node. Its old certificates then work again until they expire:

```console
$ chalkctl node unrevoke w1 --force --secrets secrets.age
unrevoked node w1; its certificates from before 2026-10-23T22:42:25Z work again until they expire: the os-ca and kubernetes-ca were not rotated since
the old w1's kubelet does not register its Node again by itself: reboot it with chalkctl node reboot w1 --no-drain, or restart its kubelet; a node installed afresh as w1 registers as usual
```

The old machine's kubelet registers its Node once as it starts, and the revocation deleted that
Node. Reboot the machine with `chalkctl node reboot w1 --no-drain`, and its Node comes back. The
deleted Node holds no pods to drain.

## Check that it worked

`chalkctl validate` lists exactly the nodes of the inventory. After `w1` is added, it lists the
three nodes before it and `w1`:

```console
$ chalkctl validate
cp1: valid for the image node-metal (node-metal on metal, root hash 98bb85096ed9), config 91a07f018b112dd92925b568babd16a428cee713fbb9e9c393ac11a2f4f5976e
cp2: valid for the image node-metal (node-metal on metal, root hash 98bb85096ed9), config 138189d54c5a0f2d15e197b349f23afbe71ded1b6179bde77bfdaf63a7ed93d7
cp3: valid for the image node-metal (node-metal on metal, root hash 98bb85096ed9), config 52a62aa28b05f78a15e09f984db2709f79a89fe79a87a87c3d8a9066f0d8e7d5
w1: valid for the image node-metal (node-metal on metal, root hash 98bb85096ed9), config d293e0172dbc6c5bef6eba9da6cd05d1ec9ae19c1a93f604662d1dc5adc2dd41
```

`chalkctl status w1` shows the new node's config, image and volumes, as
[Install a node](install.md#check-that-it-worked) describes. After `w1` is
removed again, `validate` lists the other three alone.

## What next

- [Install a node](install.md) covers maintenance mode, the system disk and `--wipe-disk`.
- [Recover a node](recover-node.md) helps a node that no longer unlocks, boots or rejoins.
- [Run a highly available control plane](../kubernetes/ha-control-plane.md) adds and removes
  control planes with the Kubernetes layer.
