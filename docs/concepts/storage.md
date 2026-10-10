---
title: "Storage and encryption"
description: "STATE, VAR and volumes, how they are encrypted and unlocked, and how they change after install"
---

# Storage and encryption

A chalkos node keeps everything it writes on partitions that are encrypted by default and unlock
through the TPM without a password. The image fixes the partitions that hold the system itself;
the node's storage section in the [cluster definition](../reference/glossary.md#cluster-definition)
declares the rest: how large [VAR](../reference/glossary.md#var) is, and which further
[volumes](../reference/glossary.md#volume) exist, on which disks, encrypted or not. This page
explains how those partitions are laid out, created, unlocked and changed, and what happens when
a disk or the TPM fails.

## What lies on a node's disks?

The system disk starts with the system region, which every node of an image shares:

- the [ESP](../reference/glossary.md#esp), which holds systemd-boot and the
  [UKIs](../reference/glossary.md#uki) of both slots, 1 GiB by default;
- two [slots](../reference/glossary.md#slot), A and B, each a pair of partitions for an image's
  erofs [store](../reference/glossary.md#store) and its [dm-verity](../reference/glossary.md#dm-verity) hash tree, 3 GiB and 128 MiB by
  default;
- [STATE](../reference/glossary.md#state), 128 MiB by default.

Their sizes are options of the image, such as
[`chalkos.disk.stateSize`](../reference/options.md#chalkosdiskstatesize), because every node of the image
must agree on them. VAR follows on the system disk and fills the rest of it by default. Volumes
go on the system disk after VAR, or each on a disk of its own.
[Image and partition layout](../reference/image-layout.md) shows the partition map.

STATE is small and holds what makes the node itself: its [identity](../reference/glossary.md#identity),
its [node certificate](../reference/glossary.md#node-certificate) and key, the OS CAs it trusts, its
[Kubernetes share](../reference/glossary.md#kubernetes-share), its storage section with the disks
it pinned, its etcd markers and pinned addresses on a [control plane](../reference/glossary.md#control-plane), and the marker that the node
is installed. VAR, mounted at `/var`, holds what the node writes while it runs: etcd's data,
containerd's images, the kubelet's state and the journal. The root file system is a tmpfs, so
nothing else survives a reboot.

## How is a node's storage declared?

The storage section sits under a role, as defaults for its nodes, and under each node. A node's
values override its role's leaf by leaf, so a node can change one volume's size or add a volume
without restating the role's other volumes; a disk reference is a leaf, so a node's reference
replaces the role's instead of merging selector keys into it. A volume with `enable = false` on a
node drops a volume its role defines.

The homelab example gives every [worker](../reference/glossary.md#worker) a volume on a SATA SSD of its own, and w1 a VAR of
200 GiB:

```nix
chalkos.roles.worker.storage.volumes.longhorn = {
  disk = { model = "Samsung SSD 870*"; type = "ssd"; };
  format = "xfs";
  mountPoint = "/var/lib/longhorn";
};
chalkos.nodes.w1.storage = {
  system.disk = "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100002";
  var.size = "200G";
};
```

A volume is formatted `ext4` by default, or `xfs`, `btrfs` or `swap`; `format = null` leaves a
raw block device, for software that wants one. A volume with a `disk` occupies that disk alone. On
each disk at most one partition, VAR included, may leave its size unset and fill the space left.
[`chalkos.nodes.<name>.storage.volumes`](../reference/options.md#chalkosnodesstoragevolumes) lists
the options; evaluation refuses duplicate mount points, reserved names such as `state` and `var`,
and mount points at `/`, `/nix`, `/state` or `/var`.

## How are disks found and pinned?

The system disk is always the disk the node booted from. udev links it as
`/dev/disk/chalk-boot-disk` and its partitions as `/dev/disk/chalk-boot/<label>`, and only that
disk gets the links, so a second disk carrying the same image never stands in for it. A node
installed in place checks that its [`storage.system.disk`](../reference/options.md#chalkosnodesstoragesystemdisk)
names the disk it booted from; the [installer](../reference/glossary.md#installer) writes the role image to the disk it names.

Other disks are named by a `/dev/` path or by a selector of `model` (a glob), `serial`, `wwn`,
`size` (a comparison such as `">= 1T"`) and `type` (`nvme`, `ssd` or `hdd`). A selector must match
exactly one disk; one that matches two is refused. A reference is resolved
once, the first time the node sees it, and the disk it found is pinned on STATE by its WWN, serial
or udev path. Every later boot looks for the pinned disk, so device names that change between
boots, or a new disk that also matches the selector, never move a volume.

A disk is taken only when it is provably unused: blkid finds nothing on it, or it carries a GPT
whose partitions all have types of this disk's definitions. A disk with other data is refused with
a message naming what it carries, and nothing is written to it.

## How are partitions created?

systemd-repart creates and grows the partitions, once per disk, from definitions chalkos renders
for the node. Three choices keep repart's matching deterministic:

- Each partition gets its own GPT type UUID derived from its label. repart assigns existing
  partitions to definitions by type, so a new volume can never take over another volume's
  partition.
- Definition files are ordered: VAR first, then the system disk's volumes in name order; a disk of
  its own holds one volume.
- Partition UUIDs derive from a seed per cluster, node and disk, so the same definition yields the
  same partition UUID on every run, and the node opens volumes on other disks by that UUID.

A volume's `repart` option passes further repart.d keys through, for example `Weight`; the keys
the typed options set cannot be overridden there.

## How are the partitions encrypted?

With [`storage.encryption.mode`](../reference/options.md#chalkosnodesstorageencryptionmode) set to
`tpm2`, the default, STATE, VAR and volumes are LUKS2 volumes whose key the
[TPM](../reference/glossary.md#tpm) seals to [PCR 7](../reference/glossary.md#pcr-7). VAR and each
volume can override the mode; STATE follows the node's. `none` leaves the partitions unencrypted,
and the cluster definition warns when STATE, which holds the node's private keys, would be one of
them. The [Security model](security.md) explains why PCR 7 is the register chalkos seals to.

Each encrypted volume has a second keyslot, the fallback, for when the TPM does not unseal, chosen
with [`storage.encryption.fallback`](../reference/options.md#chalkosnodesstorageencryptionfallback):

| Fallback | Second keyslot |
| --- | --- |
| `recovery-key` (default) | The node's [recovery key](../reference/glossary.md#recovery-key), derived from the [secrets file](../reference/glossary.md#secrets-file); [`chalkctl recovery-key`](../reference/cli/chalkctl_recovery-key.md) prints it |
| `password` | A password chalkctl asks for on the terminal, or reads from `--password-file`, at install and with each later change that adds an encrypted volume |
| `none` | None: a volume the TPM does not unseal can only be recreated empty |

[chalkd](../reference/glossary.md#chalkd) enrols the fallback with `systemd-cryptenroll` at install, unlocking each volume with the
TPM, and enrols it on every encrypted volume added later. Before it enrols a secret on a new volume
it checks that the secret opens an existing keyslot, so a mistyped password cannot leave volumes
with different fallbacks.

## What happens at boot?

The initrd opens storage in two steps, both of which print to the console:

1. `chalkos-state.service` unlocks STATE with the TPM, checks its file system with `e2fsck -p` and
   mounts it.
2. `chalkos-storage.service` reads the storage section from STATE, finds and pins the disks, runs
   repart on each, which creates missing volumes and grows those whose size grew, and unlocks,
   checks, mounts and grows VAR.

Once the system runs, a systemd generator writes a unit for every other volume: a cryptsetup
service for an encrypted one, and a mount or swap unit for a formatted one. These units are only
wanted, with `nofail`, so a missing disk fails its own units without holding up the boot. A node
that is not installed has no storage section on STATE, so `/var` stays on tmpfs until it is.

chalkos refuses to mount VAR when it should be encrypted but holds no LUKS header, and mounts an
encrypted volume only from its unlocked device, so data meant to be encrypted never lands on a
plain file system. STATE is exempt from the check, because its encryption mode is recorded on
STATE itself and cannot be known before it is mounted.

## What happens when the TPM does not unseal?

When PCR 7 changed, after a change of the [Secure Boot](../reference/glossary.md#secure-boot) keys or with Secure Boot off, the TPM
releases no key. The console then asks for the second keyslot: always for STATE, and for VAR and
the volumes unless the fallback is `none`. Typing the recovery key or password boots the node as
usual.

With fallback `none`, nothing opens a volume the TPM does not unseal. A volume's unlock unit fails,
and [`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) recreates the volume
empty. STATE and VAR cannot be recreated that way: a node whose STATE or VAR does not unlock does
not boot, and with fallback `none` it has to be reinstalled. Choose `none` only for nodes whose
disks hold nothing that a reinstall would lose.

## How does storage change after install?

[`chalkctl apply-identity`](../reference/cli/chalkctl_apply-identity.md) delivers a changed storage
section to the running node, which compares it with the section on STATE volume by volume.
Additive changes keep every byte of existing data, and the node applies them while it runs:

- a new volume, on the system disk or a new disk;
- a larger size, or a fixed size changed to fill the disk;
- a new, changed or removed mount point.

Destructive changes need a volume to be wiped:

- a volume removed or disabled;
- another disk, format or encryption mode;
- a smaller size, or a filling volume given a fixed size;
- a changed disk reference that resolves to another physical disk than the pinned one.

When the section changes any volume destructively, the node refuses the whole identity and applies
none of it, naming each volume and the command that recreates it.
[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) wipes one volume and creates
it again, empty, as the identity defines it, sealed to the TPM again and with its fallback keyslot.
Its data is lost:

```sh
chalkctl storage reset <node> <volume>
```

Changes to `repart` passthrough keys are not compared, so a section that changes only them is not
applied to the disks.

## What happens when a disk is missing?

A missing system disk stops the boot: when the disk the node booted from is not the one VAR was
pinned to, the initrd fails with a message such as `VAR lives on the disk ..., which is missing:
the node booted from ...`. A missing or unusable other disk does not: its volumes are absent, the
rest of the node runs, and the problem is recorded in `/run/chalkos/storage-status.json` and shown
by [`chalkctl status`](../reference/cli/chalkctl_status.md) as a `disk <name>: <error>` line under
the volumes. Repart failures on a disk are recorded the same way.

## Limits

- Volumes on other disks are set up in the initrd at every boot, so a storage controller whose driver
  the initrd does not carry is not supported for them.
- Volumes other than VAR are mounted with `nofail` and not ordered before `local-fs.target`. A
  workload that writes below a volume's mount point needs `RequiresMountsFor=` on that path, or it
  may write into the directory before the volume is mounted.
- VAR cannot be reset while the node runs, and a reset recreates a volume in place: it neither
  removes a volume nor moves it to another disk. A destructive change to VAR, removing a volume or
  moving one to another disk takes a reinstall.
- chalkos has no command that seals the keys again to a new PCR 7 value. After a change to the
  Secure Boot keys, each boot asks for the fallback until the node is reinstalled.

## Related pages

- [The image](image.md) and [Image and partition layout](../reference/image-layout.md): the
  system region.
- [Boot, health and rollback](boot-and-rollback.md): what the boot does before and after storage.
- [Security model](security.md): Secure Boot, PCR 7 and the recovery key.
- [Add storage volumes](../guides/storage-volumes.md) and
  [Recover a node](../guides/recover-node.md#the-console-asks-for-a-passphrase-at-boot).
- [`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) and
  [`chalkctl recovery-key`](../reference/cli/chalkctl_recovery-key.md).
