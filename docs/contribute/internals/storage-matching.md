---
title: "Storage partition matching"
description: "Where chalkd renders the repart definitions of VAR and the volumes and runs systemd-repart on each disk"
---

# Storage partition matching

[A disk is found once and then pinned](../../how-it-works/node/storage.md#a-disk-is-found-once-and-then-pinned) explains why the matching is deterministic; this page shows where the code makes it so.

## pkg/config renders the definitions

`pkg/config/storage.go` renders a node's storage section from its `SystemDisk` and `Volume`
documents, with the systemd-repart definitions of VAR and the volumes:

- `definition` sets each partition's `Type` to `storage.PartitionType` of its label, so each label
  has a GPT type UUID of its own.
- The system disk's definitions are named `50-var.conf` for VAR and `60-<volume>.conf` for each
  volume, so repart reads them in that order. A volume on a disk of its own gets
  `10-<volume>.conf` on that disk.
- Each disk's seed is `storage.UUIDOf` of the node's name and the disk's.

A `Volume`'s `repart` field passes further repart.d keys through, from the allowlist
`repartKeys`: keys that place or size the partition or set its GPT fields and take nothing from the
host. systemd-repart runs the definitions as root in the initrd, after STATE is open, so any other
key, such as one that copies files or blocks or makes directories, is refused, as the installer
refuses one in the system region's definitions. `typedRepartKeys` lists the keys the typed fields
set, which are refused with a message of their own: `Type`, `Label`, `Encrypt`, `TPM2PCRs`,
`Format`, `SizeMinBytes`, `SizeMaxBytes` and `MountPoint`.

## pkg/storage/node runs systemd-repart

`pkg/storage/node` runs systemd-repart once per disk to create and grow the partitions. It passes
the disk's seed as `--seed` and refuses a disk without one, since repart would otherwise pick a
seed of its own. On a disk other than the system disk it also passes `--empty=allow`, because the
disk was checked to be unused before it was pinned.
