---
title: "Roadmap and known limits"
description: "What chalkos does not do yet, and what is planned"
---

# Roadmap and known limits

This page lists the work planned for chalkos and the limits of the current code. Nothing here
has a date; the plans are ordered roughly by how much else depends on them. Each limit says what
happens today, so a cluster can be planned around it. [Contributing](index.md) says what a
change that takes one up needs.

## Planned work

Each item says what chalkos does today and what the plan changes.

### Secure Boot key enrolment

Today the firmware's [Secure Boot](../reference/glossary.md#secure-boot) keys are enrolled
out of band, in the firmware's setup or through a BMC, before a node is installed. chalkos
signs images with [`chalkctl sign`](../reference/cli/chalkctl_sign.md) and checks signatures
against the firmware's [db and dbx](../reference/glossary.md#db-and-dbx) when Secure Boot is
enforced, but it writes no Secure Boot variables. Only [chalklab](../reference/glossary.md#chalklab) enrols keys: its own
[lab](../reference/glossary.md#lab) keys, into its VMs' firmware.

The plan keeps the Secure Boot keys in the cluster's
[secrets file](../reference/glossary.md#secrets-file) and lets [chalkd](../reference/glossary.md#chalkd) apply variable updates
that chalkctl signs beforehand, so a node never holds a key that could sign for the firmware.
[`chalkos.secureBoot.enrollment`](../reference/options.md#chalkossecurebootenrollment)
and [`chalkos.secureBoot.require`](../reference/options.md#chalkossecurebootrequire) are
declared for this and appear on the options page, but nothing reads them yet.

### OCI publishing and pulls

Today chalkctl sends an image to each node over the node API, from a build on the machine that
runs it or from a directory. The plan publishes the install and upgrade payload (store, hash
tree, [UKI](../reference/glossary.md#uki) and boot loader) as an OCI artefact, which nodes pull
from a registry, with the artefact's signature checked.

### An in-cluster upgrade controller

Today [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) rolls an upgrade through a
cluster from an operator's machine. The plan adds a controller in the cluster, driven by a
`NodeUpgrade` resource, that upgrades nodes through chalkd with an
[operator](../reference/glossary.md#operator) certificate, using the OCI pulls above.

### Boot loader updates

Today an install writes systemd-boot, and an upgrade never changes it: chalkd refuses an
upgrade image that brings a boot loader. A node keeps the systemd-boot it was installed with
until it is reinstalled. The plan adds a flag to `chalkctl upgrade` that updates the boot loader
with an [A/B](../reference/glossary.md#a-b) scheme of its own, so a boot loader that does not start leaves the old one to fall
back to.

### More platforms

chalkos defines the platforms `metal` and `kvm`. Planned are ESXi and cloud platforms, PXE and
HTTP boot, and compressed images for distribution.

### Smaller items

| Item | What it adds |
| --- | --- |
| BGP announcement of [VIPs](../reference/glossary.md#vip) | A VIP mode besides `l2`, and one in which an extension announces the addresses. |
| Delta transfers | Upgrades that send only the changed parts of the store, instead of the whole store to every node. |
| Signed PCR 11 policies | Keys sealed to the signed image as well as to [PCR 7](../reference/glossary.md#pcr-7). |
| KMS and SSH unlock | Unlocking encrypted partitions in the initrd through a key management service, or over SSH, besides the [TPM](../reference/glossary.md#tpm) and the [recovery key](../reference/glossary.md#recovery-key). |
| Adding an IP family | Turning a single-stack cluster into a dual-stack one while it runs. |
| etcd backup and restore | Snapshots of etcd and a restore into a new [control plane](../reference/glossary.md#control-plane). |
| A node-local registry cache | A cache of container images on each node. |
| Cilium | A CNI besides flannel. |
| Bootstrap through TPM attestation | Nodes that install themselves from a management server that verifies their TPM. |
| External secret sources | The secrets file's contents in Vault or OpenBao. |

## Known limits

These limits hold in the current code, grouped by area.

### Security

- Keys of encrypted partitions are sealed to PCR 7 alone. Without Secure Boot enforced, an
  operator can install any image and it unseals [STATE](../reference/glossary.md#state) and
  [VAR](../reference/glossary.md#var); [Security model](../concepts/security.md)
  explains the consequence.
- Single certificates are not revoked. Access is revoked by rotating the CA that issued it with
  [`chalkctl rotate`](../reference/cli/chalkctl_rotate.md), and [rotations](../reference/glossary.md#rotation) run when an admin
  starts them, not on a schedule.

### Upgrades

- An upgrade sends the whole store, its hash tree and the UKI to each node, about 300 MiB for
  a Kubernetes role's image as [The image](../concepts/image.md#how-large-is-an-image) measures it.
- The boot loader is never updated after install, as above.

### Networking

- A control plane's addresses are pinned when it becomes an [etcd member](../reference/glossary.md#etcd-member), so its addresses cannot
  change while it runs. Changing them takes
  [`chalkctl etcd leave`](../reference/cli/chalkctl_etcd_leave.md) and a reinstall.
- A cluster's IP families are fixed when it is created.
- The firewall opens etcd's ports 2379 and 2380 on control planes to every source, not only to
  the other control planes; etcd accepts only certificates of its CA there.
- The firewall opens the NodePort range for TCP only.
- VIPs are announced on the local network alone, with ARP and IPv6 neighbour advertisements.

[Ports and firewall](../reference/ports-and-firewall.md) lists the rules and
[Networking](../concepts/networking.md) the design behind them.

### Storage

- Volumes other than VAR are mounted with `nofail`, so `local-fs.target` does not wait for
  them, and a service that writes below a volume's mount point needs
  `RequiresMountsFor=` on that path, or it writes to the directory underneath while the volume
  is missing.
- chalkos-storage finds every disk and creates and grows its volumes in the initrd at boot. A
  disk behind a storage controller whose driver the initrd does not carry is not found there,
  and the node's storage status reports it as failed for that boot.

[Storage and encryption](../concepts/storage.md) explains volumes and their encryption.

### Platforms and development

- The flake builds for x86-64 Linux alone, so images, chalkctl, chalklab and labs are x86-64
  only. The code reads arm64's partition types and boot loader name, but no arm64 image is built
  or tested.
- Changing a node's [platform](../reference/glossary.md#platform) is a reinstall: chalkd
  refuses an [identity](../reference/glossary.md#identity) or an image of another platform than the one it runs.
- CI builds and publishes the documentation alone. The flake checks, with their VM tests, run on
  a developer's machine, because they need `/dev/kvm`.
- The documentation is not versioned; it describes `main`.
