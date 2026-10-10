---
title: "Guides"
description: "Step-by-step guides for installing, upgrading and operating chalkos clusters"
---

# Guides

Each guide solves one task on a real cluster, from a starting point it names at the top. They
assume a [cluster definition](../reference/glossary.md#cluster-definition) in a flake and the
cluster's [secrets file](../reference/glossary.md#secrets-file); the
[quick start](../getting-started/index.md) creates both for a lab, and
[The cluster definition](../concepts/cluster-definition.md) explains what they hold.

## Install a cluster

- [Install on bare metal](install-bare-metal.md): build and sign the installer, boot it on each
  machine and install the nodes from your workstation.
- [Run on KVM, Proxmox or libvirt](kvm-proxmox-libvirt.md): run nodes as virtual machines with
  Secure Boot and a virtual TPM, from a signed `kvm` image or the installer.
- [Plan a production cluster](production-cluster.md): decide roles, addresses, storage, keys and
  time sources before the first install.
- [Run a highly available control plane](ha-control-plane.md): run three control planes behind
  a virtual IP, and add, replace or remove one.

## Customise the images

- [Customise a role](customise-role.md): add packages, services, kernel modules and Kubernetes
  objects to a role's image, and pass per-node values to its units.
- [Customise the installer](customise-installer.md): give the installer static addresses,
  VLANs, bonds, drivers or another console.
- [Support additional hardware](additional-hardware.md): add drivers, firmware and platforms the
  base images do not carry.
- [Add storage volumes](storage-volumes.md): size VAR and add encrypted or raw volumes on the
  system disk or on other disks.
- [Sign images for Secure Boot](secure-boot-signing.md): create a db key, get firmware to trust
  it and sign images and the installer with it.

## Operate a cluster

- [Upgrade a cluster](upgrade-cluster.md): roll a new chalkos or Kubernetes version through the
  nodes, one at a time or in batches.
- [Rotate certificates and CAs](rotate-certificates.md): issue client files, renew node
  certificates and rotate the cluster's CAs and keys.

## Recover from failures

- [Recover a node](recover-node.md): bring back a node that does not unlock, boot, renew its
  certificate or rejoin.
- [Troubleshooting](troubleshooting.md): find out why a node or the cluster misbehaves, starting
  from `chalkctl status`.
