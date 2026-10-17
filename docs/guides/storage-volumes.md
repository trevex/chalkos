---
title: "Add storage volumes"
description: "Size VAR and add encrypted or raw volumes on the system disk or on other disks"
---

# Add storage volumes

A node keeps what it writes on [VAR](../reference/glossary.md#var), which fills the rest of its
system disk unless you size it. Storage for a CSI driver, local persistent volumes or swap goes
on [volumes](../reference/glossary.md#volume): partitions on the system disk or whole disks of
their own, encrypted or not, formatted and mounted or left raw. This guide declares them in the
node's [config](../reference/glossary.md#config), applies them to a running node, and recreates a
volume when a change cannot be applied in place.

A node's storage is two kinds of [config document](../reference/glossary.md#config-document):
one [`SystemDisk`](../reference/kinds.md#systemdisk), which names the system disk and sets VAR
and the encryption, and a [`Volume`](../reference/kinds.md#volume) per further volume, named by
its `metadata.name`. Volumes are created with systemd-repart: by
[chalkd](../reference/glossary.md#chalkd) at install and when
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) delivers a change, and by
`chalkos-storage.service` in the initrd at every boot. repart only ever adds partitions or grows
them. A change that would shrink, reformat or re-encrypt a volume is refused until you reset
that volume, which deletes its data; one that removes a volume or moves it to another disk takes a
reinstall.

## Before you begin

- The cluster's [inventory](../reference/glossary.md#inventory), `chalkos.yaml`, with the YAML
  files of its nodes, and its [secrets file](../reference/glossary.md#secrets-file):
  `chalkctl apply-config` needs the secrets file.
- For a node that is installed, a reader [client file](../reference/glossary.md#client-file) or
  the secrets file to list its disks, and an admin client file or the secrets file to reset a
  volume.
- [Storage and encryption](../concepts/storage.md) explains how disks are pinned and how volumes
  unlock; [Config and the inventory](../concepts/config.md) explains how a node's files become
  its config.

## Find the disks

[`chalkctl disks`](../reference/cli/chalkctl_disks.md) lists a node's disks with the properties
a storage document selects disks by, the partitions on them and what each disk is used for:

```console
$ chalkctl disks w1
DEVICE            SIZE    TYPE         MODEL                    SERIAL          WWN                   USE
/dev/nvme0n1      1.8T    nvme         Samsung SSD 990 PRO 2TB  S7KHNJ0W100002  eui.0025385b11b0c4d2  boot
  /dev/nvme0n1p1  1G      esp          vfat
...
/dev/sda          931.5G  ssd          Samsung SSD 870 EVO 1TB  S6PUNX0T812345  0x5002538f43b1c2d4
```

`boot` marks the system disk; `disk <name>` marks a disk a volume holds. A machine that the
inventory does not name yet, booted into the [installer](../reference/glossary.md#installer), is
reached at its address in [maintenance mode](../reference/glossary.md#maintenance-mode):

```sh
chalkctl disks --address=<address> --insecure --secrets=secrets.age
```

`<address>` is the address the console shows. `--insecure` prints the certificate's fingerprint,
which you compare with the console. `--secrets` gives chalkctl the certificate an installer built
with the OS CA requires.

## Write the node's storage documents

Every node's config holds exactly one `SystemDisk`. Its `disk` names the disk that holds the
system region and VAR, by path or by the selector keys below. The homelab example's `w1` keeps
its system on an NVMe disk:

```yaml title="nodes/w1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec:
  disk: {path: /dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100002}
  var: {size: 200G}
```

Put the storage documents in the node's own file, the one the inventory lists under the node's
`files`, because they name that machine's disks. A node's config is a set: the documents of all
its files together, where each `Volume` needs a name of its own and a second `SystemDisk` is
refused. Nothing merges, so a node never inherits half a volume from another file.

Nodes with the same disks can share a volume through an inventory group. The group's file holds
the `Volume` once, and each node that lists the group gets it:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  files: [cluster/*.yaml]
  groups:
    longhorn: {files: [groups/longhorn.yaml]}
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    w1: {address: 10.0.0.21, groups: [longhorn], files: [nodes/w1.yaml]}
    w2: {address: 10.0.0.22, groups: [longhorn], files: [nodes/w2.yaml]}
```

```yaml title="groups/longhorn.yaml"
# Every node of the group carries a SATA SSD for Longhorn next to its NVMe system disk.
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: longhorn}
spec:
  disk: {model: "Samsung SSD 870*", type: ssd}
  format: xfs
  mountPoint: /var/lib/longhorn
```

A node whose disk differs leaves the group out and holds its own `longhorn` volume in its own
file. Removing a volume from an installed node's files is a destructive change, which the node
refuses, and no command removes a volume from a running node, so decide which nodes get a volume
before you install them.

## Size VAR

`spec.var.size` of the `SystemDisk` limits VAR; leaving it out, or `null`, fills the rest of the
system disk. Sizes are a number with an optional `K`, `M`, `G`, `T` or `P`, in units of 1024, as
`200G` in the document above.

At most one volume per disk may fill its disk, and VAR counts, so a volume on the system disk
needs a size as long as VAR has none, or the other way round. VAR holds etcd, containerd's images
and the logs: give a [control plane](../reference/glossary.md#control-plane) or a
[worker](../reference/glossary.md#worker) with large images at least tens of gigabytes. VAR
grows later without losing data. It does not shrink: shrinking is destructive, and VAR cannot be
reset while the node runs, so a smaller VAR means reinstalling the node.

## Add a volume on the system disk

A `Volume` without `disk` goes on the system disk, behind VAR, so VAR needs a size:

```yaml title="nodes/w1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: scratch}
spec:
  size: 100G
  mountPoint: /var/lib/scratch
```

The volume's `metadata.name` is its partition label: lower-case letters, digits and dashes,
starting and ending with a letter or digit, at most 32 characters. `esp`, `store`,
`store-verity`, `state`, `var` and `system` are taken.

## Put a volume on a disk of its own

A volume with `disk` takes that disk alone, as its only partition. The reference is either `path`,
a path below `/dev`, preferably one below `/dev/disk/by-id`, or a selector of properties
`chalkctl disks` shows, never both:

| Key | Matches |
| --- | --- |
| `model` | The model, as a shell pattern: `"Samsung SSD 870*"`. |
| `serial` | The serial number, exactly. |
| `wwn` | The WWN, ignoring case and a leading `0x`. |
| `size` | The size, with an optional comparison: `">= 1T"`, `"< 500G"`, `"960G"`. |
| `type` | `nvme`, `ssd` or `hdd`. |

A selector must match exactly one disk. When it matches none or several, chalkd names the disks it
found, `no disk matches ...` or `2 disks match ..., refusing to choose`, and does not create the
volume: `chalkctl apply-config` fails with the message, and at boot the node starts without
the volume. Once a reference found its disk, chalkd pins the disk on
[STATE](../reference/glossary.md#state) by its WWN, serial number and path, and finds it by the
strongest of those from then on, even when the device names change.

chalkd partitions a new disk only when it is empty: blkid finds nothing on it, or it carries a
GPT whose partitions all have the types of this volume. A disk with other data is refused with
`carries data ...; wipe it or reference another disk`, so a wrong selector never overwrites
data. Wipe a disk you mean to reuse before you reference it.

Two volumes may not reference the same disk, and a volume's disk may not be the system disk;
leave `disk` out to place a volume there.

## Choose a format and a mount point

`format` is `ext4` by default, or `xfs`, `btrfs` or `swap`. `null` leaves a raw block device for
software that brings its own format, such as Ceph's OSDs:

```yaml title="nodes/w1.yaml"
# A whole disk for a Ceph OSD, which formats it itself.
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: osd}
spec:
  disk: {serial: S6PUNX0T812345}
  format: null
```

`mountPoint` mounts the volume; leaving it out leaves the volume unmounted. The rules come from
what the unit files and the image allow:

- an absolute path of letters, digits, `.`, `_`, `-` and `/`;
- not `/`, `/nix`, `/state` or `/var`, and not below `/nix` or `/state`; below `/var` is fine;
- unique on the node, `/var` included;
- none on a raw or swap volume. A swap volume is activated as swap.

The mounts are `nofail` and not ordered before `local-fs.target`, so a missing disk does not stop
the boot. The cost is that nothing waits for them. A service you add to an image that writes
below a mount point needs `RequiresMountsFor=` on that path, or it writes into the directory on
VAR underneath. The kubelet is not ordered after them either, so a pod with a host path below a
mount point can start before the volume is mounted.

## Choose the encryption

A volume is encrypted like the node, by `spec.encryption.mode` of the `SystemDisk`, which is
`tpm2` by default: LUKS2 with a key the [TPM](../reference/glossary.md#tpm) seals to
[PCR 7](../reference/glossary.md#pcr-7). A volume's own `spec.encryption.mode`, and
`spec.var.encryption.mode` of the `SystemDisk` for VAR, override it:

```yaml title="groups/longhorn.yaml"
# The replicas on this disk are not secret; they need no TPM-sealed key.
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: longhorn}
spec:
  disk: {model: "Samsung SSD 870*", type: ssd}
  format: xfs
  mountPoint: /var/lib/longhorn
  encryption: {mode: none}
```

Every new encrypted volume also gets the node's
[fallback keyslot](../reference/glossary.md#fallback-keyslot), set by `spec.encryption.fallback`
of the `SystemDisk`: its [recovery key](../reference/glossary.md#recovery-key) by default, a
password, or none. It unlocks the volume when the TPM does not. A node whose fallback is a
password needs it again whenever a new encrypted volume appears: chalkctl asks for it, or reads it
from `--password-file`.

## Pass extra repart keys

`repart` adds keys to the volume's systemd-repart definition; the keys the typed fields set
override it:

```yaml title="nodes/w1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: scratch}
spec:
  size: 100G
  mountPoint: /var/lib/scratch
  repart: {Weight: "2000"}
```

Values are strings, as repart.d files hold them. `Type`, `Label`, `Encrypt`, `Format`,
`SizeMinBytes`, `SizeMaxBytes` and `MountPoint` come from the typed fields, and setting them here
is refused. chalkd does not compare repart keys when the definition changes, so a changed key on
an existing volume has the effect systemd-repart gives it on an existing partition, which for
most keys is none.

## Apply the change

Check the node's config first. [`chalkctl validate`](../reference/cli/chalkctl_validate.md)
checks every node's set against the schemas of its image, offline, and lists every storage
problem of a node under `invalid storage:`, such as a mount point used twice or two volumes that
fill the system disk:

```sh
chalkctl validate w1
```

Then deliver the node's config:

```console
$ chalkctl apply-config w1 --secrets secrets.age
volume scratch: new volume
w1 runs config 45095fb6414e5f6744a8bf134254107d7d943537457d513fc51e7440deace75f
```

chalkd writes the new definitions, runs systemd-repart on each disk, enrols the fallback
keyslot on new encrypted volumes and starts their mount units. One line names each changed
volume and why it changed; further lines name units it restarted because what they read
changed.

The changes chalkd applies while the node runs, and those it refuses, are these:

| Change | Applied while the node runs |
| --- | --- |
| A new volume, also on a new disk | yes |
| A larger size, or no size where a size was, of a volume or of VAR | yes, the partition grows |
| Another mount point | yes, the volume is mounted again |
| A smaller size, or a size where none was | no |
| Another format or encryption mode | no |
| Another disk, or a reference that now finds another disk | no |
| A changed reference that cannot be resolved, meaning one that finds no disk, more than one, or names something other than a whole disk | no |
| A volume removed | no |
| Any change to a volume whose disk is not plugged in | no, until the disk is back |

When a change is destructive, the node refuses the whole config and applies none of it,
naming the volume and what carries the change out:

```text
the config takes chalkctl storage reset: Volume scratch: format xfs instead of ext4; reset it with chalkctl storage reset w1 scratch
```

The image's storage [change rules](../concepts/rules.md) decide which changes the node refuses.

## Reset a volume

[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) wipes one volume and
creates it again, empty, as the node's `Volume` document in the inventory's files now defines it.
Its data is lost.

!!! danger "The volume's data is gone"

    A reset wipes the partition before it creates the new one. Move the data away, or drain the
    workloads that use it, first.

```console
$ chalkctl storage reset w1 scratch --secrets secrets.age
volume scratch of w1 was wiped and created again
```

An encrypted volume is sealed to the TPM again and gets its fallback keyslot back, so the command
needs the secrets file for a node whose fallback is its recovery key, and the password for one
whose fallback is a password. Once the reset is done, run `chalkctl apply-config` for the rest
of the config.

A reset recreates a volume in place. It refuses VAR, which the running node uses, a volume the
node's config no longer has, and a volume that moves to another disk. Those changes need the node
reinstalled with the new layout. It also refuses while the config changes other volumes
destructively too, naming them (`a reset of volume scratch does not carry out the config: ...`),
so one reset applies one volume's change: undo the other volumes' changes in the files, reset
this volume, then make the other changes again one at a time.

A reset also refuses a volume whose disk is not plugged in, and a volume whose changed disk
reference cannot be resolved, meaning one that finds no disk, more than one, or names something
other than a whole disk. It names the reason
(`Volume longhorn: disk reference ... cannot be resolved: ...`). Fix the reference in the
files, or plug the disk in, and reset again.

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

`missing` means the node never created the volume's partition, `not mounted` that it exists but its
mount failed or its disk is gone, and `present` that it exists and has no mount point, as a raw
volume does. A line `disk <name>: <problem>` after the table names a disk chalkd could not use, such
as one whose pinned disk is gone or one that carries other data.

## If something goes wrong

A disk whose storage controller has no driver in the initrd is not found at boot, so its volumes are
not mounted, or missing when they were never created, and a `disk` line names it; chalkos supports
only controllers whose drivers the initrd carries.

A pinned disk that failed and was replaced leaves its volumes `not mounted`, with a `disk` line
that names the missing pinned disk. chalkd never moves a volume to
another disk by itself, and a reset refuses a volume whose pinned disk is gone, so the node is
reinstalled to use the new disk.
[Recover a node](recover-node.md) covers volumes that do not unlock, and
[Troubleshooting](troubleshooting.md) starts from symptoms.

## What next

- [Storage and encryption](../concepts/storage.md) explains pinning, unlocking and the
  fallback.
- [Config kinds](../reference/kinds.md#systemdisk) lists every field of `SystemDisk` and
  `Volume` with its default.
- [Recover a node](recover-node.md) unlocks a node whose TPM refuses.
