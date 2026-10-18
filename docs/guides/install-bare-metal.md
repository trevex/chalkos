---
title: "Install on bare metal"
description: "Install chalkos on physical machines from the installer ISO"
---

# Install on bare metal

This guide installs a cluster on physical machines. You build the cluster's
[installer](../reference/glossary.md#installer) and its image, sign the installer for
[Secure Boot](../reference/glossary.md#secure-boot), boot it from a USB stick on each machine,
and run [`chalkctl install`](../reference/cli/chalkctl_install.md) from your workstation, which
streams the node's image to the machine and writes it to the system disk. The machine then
reboots into its image, and the first [control plane](../reference/glossary.md#control-plane)
is bootstrapped. The installer takes an empty system disk, and replaces one that holds data only
when you say so.

## Before you begin

You need:

- The cluster's repository: a flake that builds its images with `chalkos.lib.mkImages` and
  exposes them and the installer as packages, an [inventory](../reference/glossary.md#inventory)
  that names every node with its address, image and files, and each node's YAML with its `Node`,
  `SystemDisk` and `Network` documents. [Plan a production cluster](production-cluster.md) lays
  one out, and [Config and the inventory](../concepts/config.md) explains the files. The commands
  below run in the repository's directory, where `chalkos.yaml` is.
- The cluster's [secrets file](../reference/glossary.md#secrets-file), `secrets.age`, written once
  with [`chalkctl gen secrets`](../reference/cli/chalkctl_gen_secrets.md), and the
  [`osCA`](../reference/options.md#osca) argument of `mkImages` set to its public part,
  `./secrets.pub.json`, tracked by git. The images and the installer then carry the cluster's
  [OS CA](../reference/glossary.md#os-ca), so a machine in
  [maintenance mode](../reference/glossary.md#maintenance-mode) accepts only your chalkctl.
- Machines with UEFI firmware, Secure Boot and a [TPM](../reference/glossary.md#tpm) 2.0. The
  [requirements](../getting-started/requirements.md) list the rest.
- A Secure Boot [db](../reference/glossary.md#db-and-dbx) key and certificate, `db.key` and
  `db.crt`, and the certificate enrolled in each machine's firmware.
  [Sign images for Secure Boot](secure-boot-signing.md) creates them and explains the enrolment,
  which chalkos does not do for you.
- A USB stick larger than the installer ISO, about 450 MiB, or a BMC that mounts an ISO as
  virtual media.

!!! note "Installing without Secure Boot"

    The steps work on machines with Secure Boot off: leave out the signing. The node then checks
    no signatures, seals its disk keys to the Secure-Boot-off state of
    [PCR 7](../reference/glossary.md#pcr-7), and anyone holding an operator certificate can run
    an image of their own on it. [Security model](../concepts/security.md) explains why.

## Build the installer

The installer is a NixOS image that runs [chalkd](../reference/glossary.md#chalkd) in
maintenance mode and never touches the medium it boots from. One installer serves every
[variant](../reference/glossary.md#variant) and [platform](../reference/glossary.md#platform) of
the cluster. Build it from the repository's directory:

```console
$ nix build .#installer -o installer
$ ls installer/
chalkos-installer.iso  chalkos-installer.iso.json  chalkos-installer.raw  metadata.json  repart-output.json
```

The `.iso` is a hybrid image: it boots from a CD, from virtual media and written to a USB stick.
The `.raw` is the same system as a plain disk image, for machines that boot better from one. The
installer gets its addresses by DHCP on every Ethernet port;
[Customise the installer](customise-installer.md) gives it static addresses, VLANs, bonds or
extra drivers.

chalkos also builds a generic installer without an OS CA,
`nix build github:trevex/chalkos#installer`. It accepts any client until it installs a node, so
anyone on its network who reads the fingerprint can install on the machine. Use the cluster's own
installer where you can.

## Build the image and check the config

Build the image the nodes run into the directory the inventory names for it, here
`images/node-metal` for `node-metal: {source: {directory: images/node-metal}}` in the inventory's
`spec.images`:

```sh
nix build .#node-metal -o images/node-metal
chalkctl validate
```

[`chalkctl validate`](../reference/cli/chalkctl_validate.md) checks every node's config against
that image without reaching a node, so a mistake in the YAML shows before any machine boots.

## Sign the installer

The firmware boots the installer only when its boot loader and
[UKI](../reference/glossary.md#uki) are signed by a certificate in db. The build output is
read-only, so sign a copy, with the [`chalkctl sign`](../reference/cli/chalkctl_sign.md) command
and the `.iso.json` that describes the ISO's partitions:

```sh
cp --sparse=always installer/chalkos-installer.iso installer.iso
chmod u+w installer.iso
chalkctl sign --image=installer.iso --repart-json=installer/chalkos-installer.iso.json \
  --key=db.key --cert=db.crt
```

Sign the installer with the same key as the images. The installer seals the node's
[STATE](../reference/glossary.md#state) to PCR 7 as it measures it while the installer runs, and
PCR 7 includes the certificate that verified the boot binaries. An image signed with another key
boots, but its first boot finds PCR 7 changed and asks for the
[recovery key](../reference/glossary.md#recovery-key) on the console.

## Write the installer to a USB stick

Write the signed ISO to the whole stick, not to a partition of it:

```sh
sudo dd if=installer.iso of=/dev/<usb-disk> bs=4M conv=fsync status=progress
```

`<usb-disk>` is the stick's device, for example `sdb`; `lsblk` lists the candidates. Everything
on the stick is lost. With a BMC, mount `installer.iso` as virtual media instead.

## Boot the installer

Boot each machine from the stick, through the firmware's boot menu. The installer's kernel writes
to the screen and to the first serial port, where a BMC's serial-over-LAN shows it. Once chalkd
starts, it prints its mode and the SHA-256 fingerprint of its self-signed certificate, and then
its addresses with the fingerprint whenever they change:

```text
chalkd: maintenance mode, accepting clients of the OS CA; certificate fingerprint <fingerprint>
chalkd: addresses 192.168.1.50; certificate fingerprint <fingerprint>
```

Note both. The fingerprint is how chalkctl knows it talks to this machine and not to something in
the network path; the certificate lives in memory, so a reboot of the installer prints a new
one. When the first line says `accepting any client`, the installer has no OS CA: it is the
generic one, or the flake's `mkImages` has no `osCA`.

## Choose the system disk

The node's system disk is `spec.disk` of the [`SystemDisk`](../reference/kinds.md#systemdisk)
document in its file, and the installer writes only to that disk. List the machine's disks with
[`chalkctl disks`](../reference/cli/chalkctl_disks.md), at the address and fingerprint from the
console:

```sh
chalkctl disks --address=<address> --fingerprint=<fingerprint> --secrets=secrets.age
```

`--secrets` gives chalkctl the certificate the installer asks for, and `--address` reaches the
machine at the address the installer took, which is not yet the one the inventory names. The
output has one line per disk, with its device, size, type (`nvme`, `ssd` or `hdd`), model, serial
number, WWN and use, and one indented line per partition. The installer's own medium shows the
use `boot`.

Name the disk by something that survives a reboot and a moved cable: a `/dev/disk/by-id/` path,
or a selector on the serial number, model, WWN, size or type. Device names such as `/dev/sda`
can change between boots.

```yaml title="nodes/cp1.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: controlplane
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec:
  disk: {serial: S7KHNJ0W100001}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-uplink}
spec:
  match: {name: enp1s0}
  network: {address: [10.0.0.11/24], gateway: 10.0.0.1}
```

chalkctl reads the file as it is when a command runs; check it again with `chalkctl validate cp1`.

## Install the node

Run `chalkctl install` with the node's name, the installer's address and the fingerprint:

```sh
chalkctl install cp1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint> \
  --sign-key=db.key --sign-cert=db.crt
```

chalkctl takes the image from the directory of the node's image in the inventory, refuses it unless
it is the build whose root hash that directory's `metadata.json` names, signs the image's UKI and
boot loader with the db key, and checks them against the certificate `--sign-cert` names. It then
sends the node its [config](../reference/glossary.md#config), the documents of the files the
inventory gives it, checked against the image's schemas, a
[node certificate](../reference/glossary.md#node-certificate), the OS CA, the secret of the
second keyslot and, on a node with Kubernetes, the node's
[Kubernetes share](../reference/glossary.md#kubernetes-share), followed by the image's store,
hash tree, UKI and boot loader. It does not send the whole raw image, only the compressed store
and what boots it. chalkctl prints:

```text
installing cp1 onto serial "S7KHNJ0W100001" from /home/alice/prod/images/node-metal: chalkos 81bd02c4a9e1 (0f1e2d3c4b5a), sending 308281344 bytes of store, hash tree, UKI and boot loader
cp1 is installed and reboots
chalkctl recovery-key cp1 prints the key that unlocks it when its TPM fails
```

`81bd02c4a9e1` begins the image's root hash and `0f1e2d3c4b5a` the commit it was built from, the
`revision` the flake passes to `mkImages`. The installer writes the image's UKI as
`chalkos_1.efi`, the node's first boot entry.

On the machine, the installer:

1. refuses the disk it runs from, and any disk that holds data (see below);
2. lays out the system region with the image's own partition definitions: the
   [ESP](../reference/glossary.md#esp), [slot](../reference/glossary.md#slot) A, an empty slot B
   and STATE, encrypted and sealed to PCR 7 unless the `SystemDisk` turns encryption off;
3. writes slot A and verifies it from the disk against the
   [root hash](../reference/glossary.md#root-hash);
4. writes the UKI, after checking its signature against the firmware's db and dbx when Secure
   Boot is enforced, so the machine never reboots into an image its firmware refuses;
5. writes the config and the certificates to STATE, creates
   [VAR](../reference/glossary.md#var) and the node's other volumes, and enrols the second
   keyslot unless the fallback is `none`;
6. writes the boot loader, adds a UEFI boot entry named `chalkos` and boots it next, and writes
   the marker that the node is installed, last.

The machine reboots into its image. The boot entry the install creates goes first in the boot order,
so the stick may stay in; remove it before the next install elsewhere. After the reboot the node
runs in [normal mode](../reference/glossary.md#normal-mode) with the network of its config, and
chalkctl reaches it at the address the inventory names.

`--fingerprint` is the way to pin the node. When reading the console is impractical, `--insecure`
accepts any certificate and prints the fingerprint it saw, which you then compare with the
console; chalkctl then sends the secrets over a second connection pinned to that fingerprint.
With `--fingerprint`, the one connection is pinned from the start.

## Continue an install that stopped

An install that stops halfway, because chalkctl was interrupted, the network dropped or the
machine lost power, continues where it stopped: run the same `chalkctl install` command again,
with the fingerprint the installer prints now. The installer recognises a disk laid out by an
earlier attempt with the same image's partition definitions, skips a slot that holds the image
already, and logs `continuing the install on <disk>` on the console. Until the boot loader is
written, the disk has nothing the firmware boots, so a machine never boots a half-installed node.

## Replace what a disk holds

The installer takes a disk on which `blkid` finds no signature or only an empty partition table, or
one an earlier attempt of this install left. It refuses any other with a message that names what it
found, for example:

```text
the target disk /dev/nvme0n1 (model "...", size ..., serial "...") carries data (dos); pass --wipe-disk to replace it
the target disk /dev/nvme0n1 (model "...", size ..., serial "...") is not one an earlier install of this image left: ...; pass --wipe-disk to replace it
the target disk /dev/nvme0n1 (model "...", size ..., serial "...") holds an installed node; pass --wipe-disk to replace it
```

`--wipe-disk` lets the installer clear the disk's partition tables and lay it out anew.

!!! danger "--wipe-disk destroys the disk's data"

    With `--wipe-disk`, the installer replaces whatever the disk holds, an installed node
    included, and nothing on it can be recovered. Check the disk in `chalkctl disks` first. A
    disk on which `blkid` finds no signature counts as empty without the flag, even when it
    holds data.

To reinstall a control plane that is a member of etcd, take it out of etcd first with
[`chalkctl etcd leave`](../reference/cli/chalkctl_etcd_leave.md); [Recover a node](recover-node.md)
covers reinstalls.

## Bootstrap the first control plane

A cluster starts once, on one control plane. When `cp1` is back up in normal mode, run
[`chalkctl bootstrap`](../reference/cli/chalkctl_bootstrap.md):

```sh
chalkctl bootstrap cp1 --secrets=secrets.age
```

The node starts etcd as its first member, starts the API server, controller-manager and
scheduler, and applies chalkos's addons and the objects of the cluster's `Addon` documents.
chalkctl prints `bootstrapping cp1; the control plane pulls its images and starts`, waits, and
ends with `cp1 is bootstrapped; applied <n> objects`. The node refuses a second bootstrap, so a
cluster is never initialised twice.

## Install the other nodes

Install the remaining nodes the same way: boot the installer, read the fingerprint, choose the
disk, run `chalkctl install`. They need no bootstrap. Workers join the cluster at the cluster
endpoint with the kubelet certificate chalkctl gave them, and further control planes join etcd
and the control plane on their own; [Run a highly available control plane](ha-control-plane.md)
covers the second and third.

## Get a kubeconfig and client files

[`chalkctl kubeconfig`](../reference/cli/chalkctl_kubeconfig.md) writes a kubeconfig with a
cluster-admin certificate, valid for a year, to `./kubeconfig`:

```sh
chalkctl kubeconfig --secrets=secrets.age
kubectl --kubeconfig kubeconfig get nodes
```

Day-to-day commands such as `status`, `logs`, `reboot` and `upgrade` work without the secrets
file. Give each person a [client file](../reference/glossary.md#client-file) of the
[client role](../reference/glossary.md#client-role) they need with
[`chalkctl config new`](../reference/cli/chalkctl_config_new.md), and keep the secrets file for
installs, [rotations](../reference/glossary.md#rotation) and kubeconfigs:

```sh
chalkctl config new --name=<name> --role=operator --secrets=secrets.age
```

`<name>` is the person's name. Without `--out`, the file goes to `~/.config/chalkos/config`,
where chalkctl finds it whenever a command names no `--secrets`.

## Keep the recovery keys at hand

Each node's encrypted volumes have a second keyslot, its recovery key by default, for the day
the TPM does not unseal them: after a firmware update that changes dbx, a change of the Secure
Boot keys, or a replaced mainboard.
[`chalkctl recovery-key`](../reference/cli/chalkctl_recovery-key.md) derives it from the secrets
file:

```sh
chalkctl recovery-key cp1 --secrets=secrets.age
```

No file stores the keys, and installing never changes the secrets file, so backing up the secrets
file backs up every recovery key. When a node asks for its key, you are at its console and
perhaps without your workstation; keep the keys of the nodes where you can read them there, such
as a password manager.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) shows what a node runs: its config and
whether it is the one the inventory gives, the image it booted by its
[root hash](../reference/glossary.md#root-hash) and whether the boot was found healthy,
whether its store is the image the inventory names for it, its volumes, Kubernetes, its
certificates and any failed units. With the client file from the step before:

```sh
chalkctl status cp1
kubectl --kubeconfig kubeconfig get nodes
```

Each node shows its volumes as ready and no failed units, and kubectl lists every Kubernetes
node as `Ready`.

## If something goes wrong

chalkctl reports the installer's refusal as its error. The common ones:

| Message | Cause and fix |
| --- | --- |
| `the maintenance image trusts another OS CA than the secrets file, so it refuses chalkctl's certificate` | The installer or the image the machine runs was built before the OS CA was rotated, or from another cluster's `secrets.pub.json`. Build and sign both again from the current one. |
| `Secure Boot would refuse the UKI: ...` | The installer checked the UKI against the machine's db and dbx: the image is unsigned, or signed with a key whose certificate is not in this machine's db. Sign with the enrolled key, or enrol its certificate. |
| `Secure Boot would refuse the image's UKI: ...` or `... boot loader: ...` | chalkctl checked the UKI and the boot loader against the certificate `--sign-cert` names before sending anything: `--sign-key` was left out, so the image is unsigned, or the image is signed with another key. |
| `STATE on the target disk ... does not open on this machine, so it belongs to another node` | The disk was installed in another machine. Use `--wipe-disk` if its data may go. |
| The first boot asks for a recovery key | The installer and the image were signed with different keys, or Secure Boot changed between install and boot. Type the key `chalkctl recovery-key <node>` prints. The prompt returns at every boot until the change is undone, for example by booting an image signed with the original key, or the node is reinstalled, as [Recover a node](recover-node.md#the-console-asks-for-a-passphrase-at-boot) describes. |

[Troubleshooting](troubleshooting.md) covers a node that does not come up after the reboot.

## What next

- [Architecture](../concepts/architecture.md) follows an install step by step through chalkctl,
  chalkd and the disk.
- [Sign images for Secure Boot](secure-boot-signing.md) and
  [Customise the installer](customise-installer.md) prepare the media for more machines.
- [Run a highly available control plane](ha-control-plane.md) adds the second and third control
  planes.
- [Upgrade a cluster](upgrade-cluster.md) rolls the next image through the installed nodes.
