---
title: "Storage and GPU workloads"
description: "Run Rook and Ceph CSI and KubeVirt on chalkos nodes"
---

# Storage and GPU workloads

This page shows what Kubernetes images carry for workloads that need kernel modules from the
host, such as Ceph CSI and KubeVirt, and how to run them. A node's kernel loads modules only from
its image, and no container can load one, so the image must carry the modules these workloads
use. Each workload has a feature of the Kubernetes layer that adds them. `ceph` and `kubevirt`
are on by default.

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
