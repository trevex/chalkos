---
title: "Add storage volumes"
description: "Size VAR and add encrypted or raw volumes on the system disk or on other disks"
---

# Add storage volumes

A node keeps what it writes on [VAR](../reference/glossary.md#var), which fills the rest of its
system disk unless you size it. Storage for a CSI driver, local persistent volumes or swap goes
on [volumes](../reference/glossary.md#volume): partitions on the system disk or whole disks of
their own, encrypted or not, formatted and mounted or left raw. This guide declares them, applies
them to a running node, and recreates a volume when a change cannot be applied in place.

chalkd creates volumes with systemd-repart: at install, when
[`chalkctl apply-identity`](../reference/cli/chalkctl_apply-identity.md) delivers a change, and
again at every boot. It only ever adds partitions or grows them. A change that would shrink,
reformat, re-encrypt or move a volume is refused until you reset that volume, which deletes its
data.

## Before you begin

- The cluster definition and its [secrets file](../reference/glossary.md#secrets-file):
  `chalkctl apply-identity` needs the secrets file.
- For a node that is installed, a reader [client file](../reference/glossary.md#client-file) or
  the secrets file to list its disks, and an admin client file or the secrets file to reset a
  volume.
- [Storage and encryption](../concepts/storage.md) explains how disks are pinned and how volumes
  unlock.

## Find the disks

[`chalkctl disks`](../reference/cli/chalkctl_disks.md) lists a node's disks with the properties
a storage definition selects disks by, the partitions on them and what each disk is used for:

```console
$ chalkctl disks w1
DEVICE            SIZE    TYPE         MODEL                    SERIAL          WWN                   USE
/dev/nvme0n1      1.8T    nvme         Samsung SSD 990 PRO 2TB  S7KHNJ0W100002  eui.0025385b11b0c4d2  boot
  /dev/nvme0n1p1  1G      esp          vfat
...
/dev/sda          931.5G  ssd          Samsung SSD 870 EVO 1TB  S6PUNX0T812345  0x5002538f43b1c2d4
```

`boot` marks the system disk; `disk <name>` marks a disk a volume holds. A machine that is not in
the cluster definition yet, booted into the [installer](../reference/glossary.md#installer), is
reached at its address in maintenance mode:

```sh
chalkctl disks --endpoint=<address> --insecure
```

`--insecure` prints the certificate's fingerprint, which you compare with the console.

## Set storage on the role, override it on the node

Every storage option exists on a role and on a node. A role's values become defaults of its
nodes leaf by leaf, so a node overrides one attribute or adds a volume without restating the
role's. The exception is a volume's `disk`: a node's disk reference replaces the role's whole,
rather than merging its selector keys into the role's.

```nix title="cluster.nix"
{
  # Every worker carries a SATA SSD for Longhorn next to its NVMe system disk.
  chalkos.roles.worker.storage.volumes.longhorn = {
    disk = {
      model = "Samsung SSD 870*";
      type = "ssd";
    };
    format = "xfs";
    mountPoint = "/var/lib/longhorn";
  };

  # w3 has a different SSD.
  chalkos.nodes.w3.storage.volumes.longhorn.disk.serial = "S6PUNX0T812345";
}
```

`enable = false` on a node drops a volume its role defines. Use it before the node is installed:
on an installed node, a volume that disappears is a destructive change, and no command removes a
volume from a running node.

## Size VAR

[`storage.var.size`](../reference/options.md#chalkosnodesstoragevarsize) limits VAR; `null`, the
default, fills the rest of the system disk. Sizes are a number with an optional `K`, `M`, `G`, `T`
or `P`, in units of 1024.

```nix
{
  chalkos.nodes.w1.storage.var.size = "200G";
}
```

At most one volume per disk may fill its disk, and VAR counts, so a volume on the system disk
needs a size as long as VAR has none, or the other way round. VAR holds etcd, containerd's images
and the logs: give a control plane or a worker with large images at least tens of gigabytes. VAR
grows later without losing data. It does not shrink: shrinking is destructive, and VAR cannot be
reset while the node runs, so a smaller VAR means reinstalling the node.

## Add a volume on the system disk

A volume without `disk` goes on the system disk, behind VAR:

```nix
{
  chalkos.nodes.w1.storage = {
    var.size = "200G";
    volumes.scratch = {
      size = "100G";
      mountPoint = "/var/lib/scratch";
    };
  };
}
```

The volume's name is its partition label: lower-case letters, digits and dashes, at most 32
characters. `esp`, `store`, `store-verity`, `state`, `var` and `system` are taken.

## Put a volume on a disk of its own

A volume with `disk` takes that disk alone, as its only partition. The reference is a path below
`/dev`, preferably one below `/dev/disk/by-id`, or a selector of properties `chalkctl disks`
shows:

| Key | Matches |
| --- | --- |
| `model` | The model, as a shell pattern: `"Samsung SSD 870*"`. |
| `serial` | The serial number, exactly. |
| `wwn` | The WWN, ignoring case and a leading `0x`. |
| `size` | The size, with an optional comparison: `">= 1T"`, `"< 500G"`, `"960G"`. |
| `type` | `nvme`, `ssd` or `hdd`. |

A selector must match exactly one disk. When it matches none or several, chalkd names the disks it
found, `no disk matches ...` or `2 disks match ..., refusing to choose`, and does not create the
volume: `chalkctl apply-identity` fails with the message, and at boot the node starts without
the volume. Once a reference found its disk, chalkd pins the disk on STATE by its WWN, serial
number and path, and finds it by the strongest of those from then on, even when the device names
change.

chalkd partitions a new disk only when it is empty: blkid finds nothing on it, or it carries a
GPT whose partitions all have the types of this volume. A disk with other data is refused with
`carries data ...; wipe it or reference another disk`, so a wrong selector never overwrites
data. Wipe a disk you mean to reuse before you reference it.

Two volumes may not reference the same disk, and a volume's disk may not be the system disk; set
`disk = null` to place a volume there.

## Choose a format and a mount point

[`format`](../reference/options.md#chalkosnodesstoragevolumesformat) is `ext4` by default, or
`xfs`, `btrfs` or `swap`. `null` leaves a raw block device for software that brings its own
format, such as Ceph's OSDs.

[`mountPoint`](../reference/options.md#chalkosnodesstoragevolumesmountpoint) mounts the volume;
`null` leaves it unmounted. The rules come from what the unit files and the image allow:

- an absolute path of letters, digits, `.`, `_`, `-` and `/`;
- not `/`, `/nix`, `/state` or `/var`, and not below `/nix` or `/state`; below `/var` is fine;
- unique on the node, `/var` included;
- none on a raw or swap volume. A swap volume is activated as swap.

The mounts are `nofail` and not ordered before `local-fs.target`, so a missing disk does not stop
the boot. The cost is that nothing waits for them. A service you add to a role that writes below
a mount point needs `RequiresMountsFor=` on that path, or it writes into the directory on VAR
underneath. The kubelet is not ordered after them either, so a pod with a host path below a
mount point can start before the volume is mounted.

## Choose the encryption

A volume is encrypted like the node, by
[`storage.encryption.mode`](../reference/options.md#chalkosnodesstorageencryptionmode), which is
`tpm2` by default: LUKS2 with a key the [TPM](../reference/glossary.md#tpm) seals to
[PCR 7](../reference/glossary.md#pcr-7). A volume's own `encryption.mode`, and
`var.encryption.mode` for VAR, override it:

```nix
{
  # The replicas on this disk are not secret; they need no TPM-sealed key.
  chalkos.roles.worker.storage.volumes.longhorn.encryption.mode = "none";
}
```

Every new encrypted volume also gets the node's fallback keyslot, its
[recovery key](../reference/glossary.md#recovery-key) or its password, so it unlocks when the
TPM does not. A node whose fallback is a password needs it again whenever a new encrypted volume
appears: chalkctl asks for it, or reads it from `--password-file`.

## Pass extra repart keys

[`repart`](../reference/options.md#chalkosnodesstoragevolumesrepart) adds keys to the volume's
systemd-repart definition, merged last:

```nix
{
  chalkos.nodes.w1.storage.volumes.scratch.repart.Weight = 2000;
}
```

`Type`, `Label`, `Encrypt`, `Format`, `SizeMinBytes`, `SizeMaxBytes` and `MountPoint` come from
the typed options, and setting them here fails evaluation. chalkd does not compare repart keys
when the definition changes, so a changed key on an existing volume has the effect
systemd-repart gives it on an existing partition, which for most keys is none.

## Apply the change

Check the definition first: an invalid storage section stops evaluation with every problem
listed under `chalkos.nodes.<node>.storage is invalid`. Then deliver the node's identity:

```console
$ chalkctl apply-identity w1
volume scratch: new volume
w1 runs identity 6c1f4e0b9a7d2c35e8f0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f6
```

chalkd writes the new definitions, runs systemd-repart on each disk, enrolls the fallback
keyslot on new encrypted volumes and starts their mount units. One line names each changed
volume and why it changed; further lines name units it restarted because what they read
changed.

The changes chalkd applies while the node runs, and those it refuses, are these:

| Change | Applied while the node runs |
| --- | --- |
| A new volume, also on a new disk | yes |
| A larger size, or `null` where a size was | yes, the partition grows |
| Another mount point | yes, the volume is mounted again |
| A smaller size, or a size where `null` was | no |
| Another format or encryption mode | no |
| Another disk, or a reference that now finds another disk | no |
| A volume removed | no |

When a change is destructive, the node refuses the whole identity and applies none of it,
naming the volume:

```text
the identity changes storage destructively: scratch: format xfs instead of ext4 (destructive); reset it with chalkctl storage reset w1 scratch
```

## Reset a volume

[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) wipes one volume and
creates it again, empty, as the cluster definition now declares it. Its data is lost.

!!! danger "The volume's data is gone"

    A reset wipes the partition before it creates the new one. Move the data away, or drain the
    workloads that use it, first.

```console
$ chalkctl storage reset w1 scratch
volume scratch of w1 was wiped and created again
```

An encrypted volume is sealed to the TPM again and gets its fallback keyslot back, so the command
needs the secrets file for a node whose fallback is its recovery key, and the password for one
whose fallback is a password. Once the reset is done, run `chalkctl apply-identity` for the rest
of the identity.

A reset recreates a volume in place. It refuses VAR, which the running node uses, a volume the
definition no longer has, and a volume that moves to another disk. Those changes need the node
reinstalled with the new layout.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) lists the node's volumes, the disk each
lives on, its mount point and its state:

```console
$ chalkctl status w1
...
VOLUME    DISK      MOUNT POINT        STATE
longhorn  longhorn  /var/lib/longhorn  mounted
scratch   system    /var/lib/scratch   mounted
var       system    /var               mounted
...
```

`missing` means the volume's partition was not found, `not mounted` that it exists but its mount
failed. A line `disk <name>: <problem>` after the table names a disk chalkd could not use, such
as one whose pinned disk is gone or one that carries other data.

## If something goes wrong

A disk whose storage controller has no driver in the initrd is not found at boot, so its volumes
stay missing; chalkos supports only controllers whose drivers the initrd carries.

A pinned disk that failed and was replaced shows as missing too. chalkd never moves a volume to
another disk by itself, and a reset refuses a volume whose pinned disk is gone, so the node is
reinstalled to use the new disk.
[Recover a node](recover-node.md) covers volumes that do not unlock, and
[Troubleshooting](troubleshooting.md) starts from symptoms.

## What next

- [Storage and encryption](../concepts/storage.md) explains pinning, unlocking and the
  fallback.
- [The volume options](../reference/options.md#chalkosnodesstoragevolumes) list every option
  with its default.
- [Recover a node](recover-node.md) unlocks a node whose TPM refuses.
