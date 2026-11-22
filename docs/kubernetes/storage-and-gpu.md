---
title: "Storage and GPU workloads"
description: "Run Rook and Ceph CSI, KubeVirt and Longhorn on chalkos nodes"
---

# Storage and GPU workloads

This page shows what Kubernetes images carry for workloads that need kernel modules from the
host or programs of the host, such as Ceph CSI, KubeVirt and Longhorn, and how to run them. A
node's kernel loads modules only from its image, and no container can load one, so the image must
carry the modules these workloads use. Each workload has a feature of the Kubernetes layer that
adds them. `ceph` and `kubevirt` are on by default; `longhorn` is off.

## Before you begin

You need:

- nodes installed with an image with the Kubernetes layer, as
  [Bootstrap a cluster](bootstrap.md) sets them up;
- Helm, and the charts of the workloads you run.

## Run Rook and Ceph CSI

[`chalkos.kubernetes.ceph.enable`](../reference/image-options.md#chalkoskubernetescephenable) is
on by default. The image loads the `rbd`, `ceph` and `nbd` modules at boot, so ceph-csi, which
checks `/sys/module` before it calls `modprobe`, finds them loaded. LVM stays on for Rook's OSDs
on encrypted or metadata devices. `/lib/modules` holds the image's module tree, so the charts'
`hostPath` mounts of it work as they are.

Install Rook and Ceph CSI with their charts' default values. They need no chalkos-specific
values.

## Run KubeVirt

[`chalkos.kubernetes.kubevirt.enable`](../reference/image-options.md#chalkoskuberneteskubevirtenable)
is on by default. The image carries the KVM, vhost, VFIO and UIO modules, and the kernel loads
them when they are needed: KVM's by the CPU's features at boot, and vhost's when a virtual
machine opens `/dev/vhost-net`. Install KubeVirt with its operator's default values.

`/dev/kvm` exists only on machines whose CPU offers hardware virtualisation. A node that is
itself a virtual machine needs nested virtualisation on its host; without it, the node has no
`/dev/kvm`, and KubeVirt runs virtual machines only with software emulation, which its
`useEmulation` setting turns on.

### Pass PCI devices through to virtual machines

PCI passthrough needs the IOMMU and the `vfio_pci` module, which binds the devices. No pod can
load `vfio_pci`, so the image loads it at boot when its modules set
[`chalkos.kubernetes.kubevirt.iommu`](../reference/image-options.md#chalkoskuberneteskubevirtiommu):

```nix
chalkos.kubernetes.kubevirt.iommu = true;
```

The option also adds `intel_iommu=on iommu=pt` to the kernel's command line. AMD's IOMMU is on
unless the command line turns it off, so it needs no parameter of its own. Turn on the IOMMU in
the machine's firmware as well, as VT-d on Intel or AMD-Vi on AMD.

## Run Longhorn

[`chalkos.kubernetes.longhorn.enable`](../reference/image-options.md#chalkoskuberneteslonghornenable)
is off by default. Turn it on in the image's modules:

```nix
chalkos.kubernetes.longhorn.enable = true;
```

The image then carries what Longhorn's chart expects of a node:

- iscsid, which runs from boot, and `iscsiadm`. Longhorn finds the iscsid process and runs
  `iscsiadm` in its namespaces, which are the host's. iscsiadm's database lives in
  `/var/lib/iscsi`, and each node writes its initiator name there on its first boot.
- The NFS client, `mount.nfs`, for Longhorn's RWX volumes. Longhorn's CSI plugin mounts them with
  NFSv4.1 by running `mount` in the host's namespaces.
- Links for `iscsiadm`, `fstrim`, `cryptsetup`, `dmsetup` and `mount` in `/usr/bin` and
  `/usr/sbin`, where Longhorn finds them in the host's mount namespace.
- The `iscsi_tcp` and `dm_crypt` modules, loaded at boot, as Longhorn's node check expects
  `dm_crypt` to be loaded.

Give Longhorn's replicas a disk of their own with a Volume mounted on `/var/lib/longhorn`, the
chart's default data path, as [Add storage volumes](../operate/storage-volumes.md#put-a-volume-on-a-disk-of-its-own)
shows. A Volume mounted elsewhere needs the chart's `defaultSettings.defaultDataPath` set to its
mount point. Without a Volume, Longhorn keeps its replicas on VAR. Install Longhorn with its
chart's default values otherwise.

The NFS client mounts NFSv4 alone. The image carries no `rpc.statd`, which NFSv3's locks need.

### Run Longhorn's V2 data engine

The V2 data engine runs on SPDK, which needs huge pages and the `vfio_pci`, `uio_pci_generic`
and `nvme_tcp` modules. The `kubevirt` feature carries the VFIO and UIO modules, so keep it on.
Longhorn's node check expects the three modules to be loaded, and no pod can load a module, so
load them at boot and reserve the 2 MiB huge pages the chart's `dataEngineMemorySize` asks for,
2048 MiB by default, in the image's modules:

```nix
boot.kernelModules = [
  "vfio_pci"
  "uio_pci_generic"
  "nvme_tcp"
];
boot.kernelParams = [
  "hugepagesz=2M"
  "hugepages=1024"
];
```

Then set the chart's `defaultSettings.v2DataEngine` to `true`.

## Leave a workload's modules out of an image

An image's modules may set `chalkos.kubernetes.ceph.enable` or
`chalkos.kubernetes.kubevirt.enable` to `false`. Without `ceph`, the image loads no Ceph modules
at boot, and ceph-csi cannot load them itself, as no pod can load a module. Without `kubevirt`,
the image carries no KVM, vhost, VFIO or UIO modules, which keeps the module tree of nodes that
run no virtual machines smaller.

## What next

- [Security model](../how-it-works/node/security.md#kernel-modules-load-from-the-image-alone)
  explains why pods cannot load modules.
- [How Kubernetes runs on a node](../how-it-works/kubernetes/overview.md) describes
  `/lib/modules`, the host tools and containerd's runtime handlers.
- [Image options](../reference/image-options.md) lists every option of the Kubernetes layer.
