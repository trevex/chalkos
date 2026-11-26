---
title: "Roadmap and known limits"
description: "What chalkos does not do yet, and what is planned"
---

# Roadmap and known limits

This page lists the work planned for chalkos and the limits of the current code. Nothing here
has a date; the plans are ordered roughly by how much else depends on them. Each limit says what
happens today, so a cluster can be planned around it. [Contribute](index.md) says what a
change that takes one up needs.

## Good first changes

These changes are small, and a check proves each of them. The code or the docs need each one
today:

- Carry the kernel modules of every `NetDev` kind. A `NetDev` document's schema accepts
  `spec.l2tp`, `spec.macsec`, `spec.batmanAdvanced` and `spec.bareUDP`, but no group of
  `chalkos.kernel.groups` carries their modules, so networkd cannot create the device. Add the
  directories `net/l2tp` and `net/batman-adv` and the modules `macsec` and `bareudp` to the
  `network` group in `modules/node/kernel.nix`, and build `kernel-modules` and `image-size`.
- Open the NodePort range for UDP. `modules/node/kubernetes/firewall.nix` opens ports 30000 to
  32767 for TCP alone, as [Known limits](#networking) says. Add the same range to
  `allowedUDPPortRanges`, and update
  [Ports and firewall](../reference/ports-and-firewall.md) and this page.

## Planned work

Each item says what chalkos does today and what the plan changes.

### An in-cluster upgrade controller

Today [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) rolls an upgrade through a
cluster from an operator's machine. The plan adds a controller in the cluster, driven by a
`NodeUpgrade` resource, that upgrades nodes through chalkd with an
operator certificate, with the nodes pulling the images from a registry.

### Boot loader updates

Today an install writes systemd-boot, and an upgrade never changes it: chalkd refuses an upgrade
image that brings a boot loader. A node keeps the systemd-boot it was installed with until it is
reinstalled. The plan adds a flag to `chalkctl upgrade` that updates the boot loader with an
A/B scheme of its own, so a boot loader that does not start leaves
the old one to fall back to.

### Measured boot and attestation

Today the TPM seals the disk keys to PCR 7 alone, the node's key is a file on STATE, and nothing
outside a node can verify what it booted, as
[Chain of trust](../how-it-works/node/trust.md#limits) describes. The plan binds the keys to the
images the key holder signed and lets a verifier check a node, in the roles of RFC 9334, the IETF's
remote attestation architecture: the node is the attester, which sends evidence, and a verifier
compares the evidence with reference values. Each item builds on the ones before it:

| Item | What it adds |
| --- | --- |
| Boot phase in the policy | Disk keys sealed to the initrd's phase as well, through systemd's PCR 11 phase measurements, so root on a running node cannot unseal them again. |
| Signed PCR 11 policies | Disk keys sealed to a key that signs each image's PCR 11 values at `chalkbuild sign`, so only images the key holder signed unseal them. |
| A node key in the TPM | A node key that the TPM generates and never exports, which signs chalkd's TLS handshakes, so the node's identity cannot be copied off the machine. |
| Evidence through the node API | A method that returns a TPM quote over PCRs 4, 7 and 11 with the event log, signed by an attestation key the TPM certifies. |
| Reference values with each release | The expected measurements of each image, computed at build time and published with the release and its signatures, so a verifier needs no node to learn them: the PCR 4 event of its UKI and of the systemd-boot it carries, and its PCR 11 values. An upgrade replaces the UKI and the store but never systemd-boot, so a node's PCR 4 holds the boot loader of the release that installed it and the UKI of the image it runs, and a release carries events rather than one PCR 4 value. PCR 7 depends on each machine's Secure Boot variables, such as dbx, and not on the image, so a release carries no value for it. |
| A verifier in chalkctl | chalkctl replays the node's event log: it checks the PCR 4 events against the UKI of the node's image and the systemd-boot of any release the key holder signed, its PCR 11 against the reference values, and its PCR 7 against the Secure Boot variables in the event log and the key holder's db certificate, before it trusts the node's report of its image. |
| Bootstrap through TPM attestation | Nodes that install themselves from a management server that verifies their TPM. |
| Confidential virtual machines | An exploration of AMD SEV-SNP and Intel TDX guests with a vTPM inside the guest, such as one that COCONUT-SVSM runs, so the hypervisor no longer controls the TPM. |

### More platforms

chalkos defines the platforms `metal` and `kvm`. Planned are ESXi and cloud platforms, PXE and
HTTP boot, and compressed images for distribution.

### More router protocols

Today the router layer routes dynamically with BGP alone, through FRR, and routers share no
address. The plan adds these protocols, each as a feature an image can leave out:

| Item | What it adds |
| --- | --- |
| OSPF | OSPFv2 and OSPFv3 through FRR, beside BGP. |
| VRRP | Addresses that move to another router when one fails, and later the sync of connection tracking state between the routers. |
| LLDP | Discovery of the neighbours on the router's links, and announcement of the router to them. |

### Smaller items

| Item | What it adds |
| --- | --- |
| Delta transfers | Upgrades that send only the changed parts of the store, instead of the whole store to every node. |
| KMS and SSH unlock | Unlocking encrypted partitions in the initrd through a key management service, or over SSH, besides the TPM and the recovery key. |
| Adding an IP family | Turning a single-stack cluster into a dual-stack one while it runs. |
| etcd backup and restore | Snapshots of etcd and a restore into a new control plane. |
| Replication of the cluster registry | Images pushed to one endpoint of the cluster registry copied to the others, so an endpoint that missed a push, or a new control plane, catches up without another push. |
| Cilium | A CNI besides flannel. |
| External secret sources | The secrets file's contents in Vault or OpenBao. |

## Known limits

These limits hold in the current code, grouped by area.

### Security

- Keys of encrypted partitions are sealed to PCR 7 alone. Without Secure Boot enforced, an
  operator can install any image and it unseals STATE and
  VAR; [Security model](../how-it-works/node/security.md)
  explains the consequence.
- PCR 7 does not change after the initrd, so root on a running node can unseal the keys of STATE
  and VAR again.
- A node's key is a file on STATE, which root on the node can copy.
- No one outside a node can verify which image it booted. chalkd serves no TPM quote.
- In a virtual machine with a vTPM that the hypervisor emulates, the hypervisor's admin can unlock
  the node's disks.
  [Chain of trust](../how-it-works/node/trust.md#whoever-controls-the-tpm-controls-the-disk-keys)
  compares bare metal and virtual machines.
- Single certificates are not revoked. A client file stays valid until it expires or the OS CA is
  rotated with [`chalkctl rotate`](../reference/cli/chalkctl_rotate.md), which retires every
  client file of the old OS CA. With the Kubernetes layer,
  [`chalkctl node revoke`](../reference/cli/chalkctl_node_revoke.md) shuts out one node by name.
  Rotations run when an admin starts them, not on a schedule.
- chalkos writes no Secure Boot variable on an installed node: it applies no dbx updates and
  changes no keys there. Such a change alters PCR 7, so the node's next boot asks for its recovery
  key. No setting makes a node refuse to install without Secure Boot.
- A node pulls an image from a registry by its digest and checks no signature of the OCI artefact
  itself.

### Upgrades

- An upgrade sends the whole store, its hash tree and the UKI to each node, about 300 MiB for
  an image with Kubernetes as [The image](../how-it-works/node/image.md#every-build-leaves-room-for-the-next-image) measures it.
- The boot loader is never updated after install, as above.

### Networking

- A control plane's addresses are pinned when it becomes an etcd
  member, so its addresses cannot change while it runs.
  Changing them takes [`chalkctl etcd leave`](../reference/cli/chalkctl_etcd_leave.md) and a
  reinstall.
- A cluster's IP families are fixed when it is created.
- The firewall opens etcd's ports 2379 and 2380 on control planes to every source, not only to
  the other control planes; etcd accepts only certificates of its CA there.
- The firewall opens the NodePort range for TCP only.
- Packets a node receives lose the packet mark bit 0x02000000, which the firewall's rule for the
  control plane's ports uses, on a node that serves the cluster registry the bit 0x04000000
  of its port's rule, and on a node that runs FRR the bit 0x08000000 of BFD's rule. A network plugin of the cluster's own (`chalkos.cni.provider = "none"`)
  must not use them.
- The endpoints of the cluster registry do not replicate. Each holds what was pushed to it, and
  `chalkctl registry list` shows where they differ.
- The Kubernetes layer's BGP speaker announces the addresses of interfaces alone, such as the
  node's address and a routed VIP: no pod ranges and no LoadBalancer addresses.

[Ports and firewall](../reference/ports-and-firewall.md) lists the rules and
[Kubernetes networking](../how-it-works/kubernetes/networking.md) the design behind them.

### Storage

- Volumes other than VAR are mounted with `nofail`, so `local-fs.target` does not wait for
  them, and a service that writes below a volume's mount point needs
  `RequiresMountsFor=` on that path, or it writes to the directory underneath while the volume
  is missing.
- chalkos-storage finds every disk and creates and grows its volumes in the initrd at boot. A
  disk behind a storage controller whose driver the initrd does not carry is not found there,
  and the node's storage status reports it as failed for that boot.

[Storage and encryption](../how-it-works/node/storage.md) explains volumes and their encryption.

### Platforms and development

- The flake builds for x86-64 Linux alone, so images, chalkctl, chalklab and labs are x86-64
  only. The code reads arm64's partition types and boot loader name, but no arm64 image is built
  or tested.
- Changing a node's platform is a reinstall: chalkd
  refuses an image of another platform than the one it runs, and translates
  config for its own.
- CI runs on GitHub-hosted x86-64 runners alone. The end-to-end checks run on a pull request
  only when a label asks for them; [Continuous integration](ci.md) lists the tiers.
- The documentation is not versioned; it describes `main`.
