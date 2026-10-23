---
title: "Add storage volumes"
description: "Size VAR and add encrypted or raw volumes on the system disk or on other disks"
---

# Add storage volumes

This page is for an operator who gives nodes storage beyond VAR. At the end a node has a volume
for a CSI driver, local persistent volumes, Ceph or swap, encrypted or not, formatted and mounted
or left raw. The page adds a Longhorn disk to the homelab's `w1`, then a scratch volume on its
system disk.

A node's storage is two kinds of document. One `SystemDisk` names the system disk, sizes VAR and
sets the encryption. A `Volume` adds each further volume, named by its `metadata.name`. A volume
is a partition on the system disk, behind VAR, or a whole disk of its own.

## Before you begin

You need the workstation that [Set up your workstation](workstation.md) describes, with the secrets
file `secrets.age`. `chalkctl apply-config` needs it. `chalkctl disks` and `chalkctl status` need a
reader client file or the secrets file.

Decide which nodes get a volume before you install them. Removing a volume from a running node
takes a reinstall.

## Find the disks

`chalkctl disks` lists a node's disks, the properties a document selects a disk by, the
partitions on each and what each disk is used for. The output below is illustrative: its columns
are chalkctl's, and the disks, serials and WWNs are an example machine's.

```console
$ chalkctl disks w1
DEVICE            SIZE    TYPE         MODEL                    SERIAL          WWN                   USE
/dev/nvme0n1      1.8T    nvme         Samsung SSD 990 PRO 2TB  S7KHNJ0W100002  eui.0025385b11b0c4d2  boot
  /dev/nvme0n1p1  1G      esp          vfat
...
/dev/sda          931.5G  ssd          Samsung SSD 870 EVO 1TB  S6PUNX0T812345  0x5002538f43b1c2d4
```

`boot` marks the system disk. `disk <name>` marks a disk that the volume `<name>` holds. The SATA
SSD `/dev/sda` is unused.

A machine that runs the installer is reached at the address its console shows:

```sh
chalkctl disks --address=<address> --insecure --secrets=secrets.age
```

`--insecure` prints the certificate's fingerprint, which you compare with the console.

## Put a volume on a disk of its own

A `Volume` with `disk` takes that disk alone. The homelab's `w1` gives its SATA SSD to Longhorn:

```yaml title="nodes/w1.yaml"
# A SATA SSD for Longhorn replicas next to the NVMe system disk.
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: longhorn}
spec:
  disk: {model: "Samsung SSD 870*", type: ssd}
  format: xfs
  mountPoint: /var/lib/longhorn
```

Storage documents go in the node's own file, because they name that machine's disks. Nodes with
the same disks can share a `Volume` through an inventory group, as
[Write a node's config](node-config.md#groups-share-files-and-an-image-among-some-nodes) shows.

`disk` names the disk by `path` or by selector keys: `model`, `serial`, `wwn`, `size` and `type`.
The two forms exclude each other. `path` may be any path below `/dev` that leads to a whole disk.
A path below `/dev/disk/by-id` stays the same across boots, where `/dev/sda` may not. `wwn`
ignores case and a leading `0x`, so a WWN copied from another tool matches.
[Config kinds](../reference/kinds.md#volume) lists what each key matches.

A selector must match exactly one disk. When it matches none or several, the node creates no
volume, and the message names the disks it found: `no disk matches ...` or
`2 disks match ..., refusing to choose: ...`. chalkd partitions a disk only when it is empty, so
a wrong selector never overwrites data. A disk with other data is refused with
`carries data (...); wipe it or reference another disk`. Wipe a disk you mean to reuse first.

A volume whose disk is not found yet is left out at boot. The node boots without it,
`chalkctl status` shows the disk's problem, and the next boot looks for the disk again.

Once chalkd found the disk, it pins it on STATE by its WWN, serial number and path. From then on
it finds the disk by the strongest of those, even when the device names change.

Two volumes may not share a disk, and a volume with `disk` may not name the system disk.

## Add a volume on the system disk

A `Volume` without `disk` goes on the system disk, behind VAR:

```yaml title="nodes/w1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: scratch}
spec:
  size: 100G
  mountPoint: /var/lib/scratch
```

At most one partition per disk may fill its disk, and VAR counts. The homelab's `w1` sizes VAR at
`200G`, so the scratch volume may fill the rest, or take a size as here. Sizes are a number with
an optional `K`, `M`, `G`, `T` or `P`, in units of 1024.

The volume's name is its partition label: lower-case letters, digits and dashes, starting and
ending with a letter or digit, at most 32 characters. `esp`, `store`, `store-verity`, `state`,
`var` and `system` are taken.

## Size VAR

`spec.var.size` of the `SystemDisk` limits VAR. Without it, VAR fills the rest of the system
disk. VAR holds etcd, containerd's images and the logs, so give a control plane, or a worker with
large images, tens of gigabytes at least.

VAR grows later without losing data. It does not shrink: shrinking is destructive, and VAR cannot
be reset while the node runs. A smaller VAR takes a reinstall.

## Choose a format and a mount point

`format` is `ext4` by default, or `xfs`, `btrfs`, `swap` or `raw`. A raw volume is a block device
for software that brings its own format, such as a Ceph OSD:

```yaml title="nodes/w1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: osd}
spec:
  disk: {serial: S6PUNX0T812345}
  format: raw
```

`mountPoint` mounts the volume. Without it the volume stays unmounted. A mount point:

- is an absolute path of letters, digits, `.`, `_`, `-` and `/`;
- is not `/`, `/nix`, `/state` or `/var`, and not below `/nix` or `/state`; below `/var` is
  allowed;
- is unique on the node;
- is not set on a raw or swap volume. A swap volume is activated as swap.

The mounts are `nofail`, so a missing disk does not stop the boot. They are not ordered before
`local-fs.target`, so nothing waits for them. A service that writes below a mount point needs
`RequiresMountsFor=` on that path in its image, or it writes into the directory on VAR underneath.

!!! note "On the shipped variant, a pod can start before a volume is mounted"

    The kubelet is not ordered after the mounts. A pod with a host path below a mount point can
    start before its volume is mounted, and write to VAR.

## Choose the encryption

A volume is encrypted like the node, by `spec.encryption.mode` of the `SystemDisk`. The default,
`tpm2`, is LUKS2 with a key the TPM seals to PCR 7. A volume's own `spec.encryption.mode`
overrides it, and `spec.var.encryption.mode` of the `SystemDisk` overrides it for VAR. A volume
whose data is not secret can go without encryption:

```yaml title="nodes/w1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: longhorn}
spec:
  disk: {model: "Samsung SSD 870*", type: ssd}
  format: xfs
  mountPoint: /var/lib/longhorn
  encryption: {mode: none}
```

Every new encrypted volume also gets the node's fallback keyslot, which unlocks it when the TPM
does not. `spec.encryption.fallback` of the `SystemDisk` sets it: the recovery key by default, a
password, or none. A node whose fallback is a password needs it for each new encrypted volume.
chalkctl asks for it on the terminal, or reads it from the file `--password-file` names.

`repart` passes further keys to the volume's systemd-repart definition, such as
`repart: {Weight: "2000"}`. A key that the typed options set, such as `Format` or `MountPoint`, is
refused. The message reads `repart must not set Format; it comes from the typed options`. chalkd
does not compare the other keys when they change, so a changed key on an existing volume does what
systemd-repart does with it on an existing partition, which for most keys is nothing.

## Apply the change

Check the node's config offline first. `chalkctl validate` lists every storage problem of a node
under `invalid storage:`, such as a mount point used twice or two volumes that fill one disk:

```sh
chalkctl validate w1
```

Then deliver the config:

```console
$ chalkctl apply-config w1 --secrets secrets.age
w1: config 1 added, 0 removed, 0 changed
volume scratch: new volume
w1 runs config 45095fb6414e5f6744a8bf134254107d7d943537457d513fc51e7440deace75f
```

The first line counts the documents the config adds, removes and changes: here the `Volume`
named `scratch`. chalkd writes the new definitions and runs systemd-repart on each disk. It enrols
the fallback keyslot on new encrypted volumes and starts their mount units. One line names each
changed volume and why it changed.

A running node applies three kinds of storage change:

- a new volume, also on a new disk;
- a larger size, or no size where a size was, for a volume or VAR: the partition grows;
- another mount point: the volume is mounted again.

Every other change is destructive, and the node refuses the whole config:

- a smaller size, or a size where none was;
- another format or encryption mode;
- another disk, or a reference that now finds another disk or none;
- a volume removed from the files;
- any change to a volume whose disk is not plugged in, until the disk is back.

The refusal names the volume and what carries the change out:

```text
the config takes chalkctl storage reset: Volume scratch: format xfs instead of ext4; reset it with chalkctl storage reset w1 scratch
```

[Change a node's config](change-config.md#a-refusal-names-what-the-change-takes) explains the
refusals. VAR, a removed volume and a volume that moves to another disk take a reinstall, which
[Reinstall a node on its machine](add-remove-nodes.md#reinstall-a-node-on-its-machine) covers.

## Reset a volume

`chalkctl storage reset` wipes one volume and creates it again, empty, as the node's `Volume`
document now defines it:

```console
$ chalkctl storage reset w1 scratch --secrets secrets.age
volume scratch of w1 was wiped and created again
```

!!! danger "A reset deletes the volume's data"

    The command wipes the partition before it creates the new one. Move the data away, or stop
    the workloads that use it, first.

An encrypted volume is sealed to the TPM again and gets its fallback keyslot back. The command
needs the secrets file for a node whose fallback is its recovery key, and the password for one
whose fallback is a password. Then run `chalkctl apply-config` for the rest of the config.

A reset refuses:

- VAR, which the running node uses;
- a volume the config no longer has, or one that moves to another disk;
- a volume whose disk is not plugged in, or whose changed reference finds no disk or several;
- while the config changes other volumes destructively too:
  `a reset of volume scratch does not carry out the config: ...`.

For the last case, undo the other volumes' changes in the files, reset this volume, then make the
other changes one at a time.

## Check that it worked

`chalkctl status` lists the node's volumes, the disk each lives on, its mount point and its state:

```console
$ chalkctl status w1
...
VOLUME    DISK      MOUNT POINT        STATE
longhorn  longhorn  /var/lib/longhorn  mounted
scratch   system    /var/lib/scratch   mounted
var       system    /var               mounted
...
```

`missing` means the node never created the volume's partition. `not mounted` means the partition
exists but its mount failed or its disk is gone. `present` means it exists and has no mount
point, as a raw volume does. A line `disk <name>: <problem>` after the table names a disk chalkd
could not use.

## If something goes wrong

- `disk longhorn: pinned disk ... is missing`: the disk the volume was created on is gone, or
  its controller did not come up. chalkd never moves a volume to another disk by itself. Put the
  disk back and reboot. A replacement disk takes a reinstall.
- A disk whose storage controller has no driver in the initrd is not found at boot. Its volumes
  stay `not mounted`, or `missing` when they were never created. The image author adds the
  driver, as [Add software, drivers and kernel modules](../build/add-software.md) describes.
- A volume that does not unlock at boot leaves the console at a passphrase prompt.
  [Recover a node](recover-node.md#the-console-asks-for-a-passphrase-at-boot) covers it.

## What next

- [Storage and encryption](../how-it-works/node/storage.md) explains pinning, unlocking and the
  fallback.
- [Config kinds](../reference/kinds.md#systemdisk) lists every field of `SystemDisk` and
  `Volume`.
- [Status, logs and the console](status-and-logs.md) reads the rest of the status.
