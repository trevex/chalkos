---
title: "Install a node"
description: "Boot a physical machine from the installer and install a node of the inventory on it with chalkctl install"
---

# Install a node

This page is for an operator who turns a physical machine into a node of the inventory. At the end the node runs its image from
its system disk with its config, and chalkctl reaches it at the address the inventory names. The
page installs `cp1` of the `cluster-metal` template, as [Write a node's config](node-config.md)
describes it. [Build a cluster on real machines](../guides/cluster-on-machines.md) installs all
three of the template's machines.
[Install a node in a VM](install-vm.md) runs a node under QEMU, libvirt or Proxmox instead.

In short:

```sh
nix build .#installer -o installer
chalkbuild sign --key keys/db.key --cert keys/db.crt installer installer-signed
chalkctl disks --address=<address> --fingerprint=<fingerprint> --secrets=secrets.age
chalkctl install cp1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
chalkctl status cp1
```

With the published images of a chalkos release, the release's installer takes the place of the
first two commands.

## Before you begin

You need a workstation set up as [Set up your workstation](workstation.md) describes: chalkctl,
the repository with the inventory, the nodes' config files, the node's image as
[Choose where the image comes from](#choose-where-the-image-comes-from) describes, the secrets
file `secrets.age` and a client file. `chalkctl validate` accepts every node's config.

Images and an installer of your own carry the cluster's OS CA, as
[Build the images with the cluster's OS CA](workstation.md#build-the-images-with-the-clusters-os-ca)
describes. A machine in maintenance mode then accepts only a chalkctl that holds the secrets file
or a client file of this cluster. The published images of a chalkos release and its installer
carry none, as
[Published images or your own](../get-started/decisions.md#published-images-or-your-own)
explains.

--8<-- "docs/includes/no-os-ca.md"

[Decisions before you install](../get-started/decisions.md) lists the choices to make before the
first install, such as whether the machines enforce Secure Boot.

Each machine needs UEFI firmware, Secure Boot and a TPM 2.0. The
[requirements](../get-started/requirements.md) list the rest. For a machine that enforces Secure
Boot, you also need:

- Secure Boot keys, with the db certificate enrolled in the machine's firmware.
  [Sign images for Secure Boot](../build/sign-images.md) creates them in `keys/` and explains the
  enrolment. A machine whose firmware is in Setup Mode gets them from
  [`chalkctl secureboot enroll`](#enrol-the-secure-boot-keys) before the install;
- chalkbuild on the machine that holds `keys/db.key`, from
  `nix shell github:trevex/chalkos#chalkbuild` or the cluster flake's development shell.

A physical machine boots the installer from a USB stick larger than the installer ISO, about
450 MiB, or from a BMC that mounts an ISO as virtual media.

!!! note "Installing without Secure Boot"

    The steps work on machines with Secure Boot off: leave out the signing, and keep the
    inventory on the unsigned image directory. Anyone who holds an operator certificate can then
    run an image of their own on the node, as
    [Secure Boot limits a node to images the key holder signed](../how-it-works/node/security.md#secure-boot-limits-a-node-to-images-the-key-holder-signed)
    explains.

A machine passes through maintenance mode once: it boots the installer, which has no config,
`chalkctl install` gives it its config and secrets, and it reboots as an installed node in normal
mode. [A node passes through stages](../how-it-works/node/overview.md#a-node-passes-through-stages)
shows the whole life of a node.

## Choose where the image comes from

The inventory's `spec.images` names the node's image in one of two forms, and `chalkctl install`
takes either:

- A reference in a registry, `source.oci`, pinned by its digest: an image a chalkos release
  publishes, as [Releases](../get-started/releases.md#name-an-image-by-its-digest) shows, or your
  own, pushed as [Publish images to a registry](../build/publish-images.md) describes. chalkctl
  sends the installer the reference, and the installer pulls the image itself, so the machine
  needs a route and DNS to the registry.
- An image directory, `source.directory`: your own build or its signed copy. chalkctl reads the
  image on the workstation and sends it to the installer, so the machine needs no registry.

A site whose machines reach no registry takes an image from a registry as a directory.
[`chalkctl image pull`](../reference/cli/chalkctl_image_pull.md) writes one on the workstation, and
checks every part of the image as a node would:

```sh
chalkctl image pull ghcr.io/trevex/chalkos/kubernetes-1.37-metal:<tag>@sha256:<digest> images/node-metal
```

The inventory then names `images/node-metal` as the image's `directory` source.

## Sign the image for machines that enforce Secure Boot

Skip this step on a machine that does not enforce Secure Boot: it takes the unsigned
`images/node-metal` as it is. The published images carry no signature, so a machine that enforces
Secure Boot runs your own images, signed with your db key. The firmware of a machine that
enforces Secure Boot boots only a
boot loader and a UKI signed by a certificate in its db. Sign a copy of the build with
[`chalkbuild sign`](../reference/cli/chalkbuild_sign.md), which also checks the copy against
`db.crt`:

```console
$ rm -rf images/node-metal-signed
$ chalkbuild sign --key keys/db.key --cert keys/db.crt images/node-metal images/node-metal-signed
signed images/node-metal-signed/chalkos.raw
```

The output directory must not exist, so `rm -rf` removes the copy of an earlier build. Point the
inventory's image at the signed copy, as
[Point the inventory at the signed copy](../build/sign-images.md#point-the-inventory-at-the-signed-copy)
shows, and sign every new build again before you install or upgrade with it.

## Boot the machine into maintenance mode

A physical machine boots the installer. With the published images, download the installer of the
same release, as
[Download chalkctl and the installer](../get-started/releases.md#download-chalkctl-and-the-installer)
shows, and write its ISO to the USB stick as below. It has no OS CA, and it is not signed for
Secure Boot.

With images of your own, build the installer from the repository's directory:

```console
$ nix build .#installer -o installer
$ ls installer/
chalkos-installer.iso  chalkos-installer.iso.json  chalkos-installer.raw  metadata.json  repart-output.json
```

The `.iso` boots from a CD, from virtual media and written to a USB stick. The `.raw` is the same
system as a plain disk image, for machines that boot better from one. The installer takes its
addresses by DHCP and SLAAC; [Customise the installer](../build/customise-installer.md) gives it
static addresses, VLANs, bonds or extra drivers. With images of your own, use the cluster's
installer: the generic one of the chalkos flake, `github:trevex/chalkos#installer`, has no OS CA
and accepts any client, as the release's does.

Sign a copy of the installer's directory, its raw image and its ISO, with the same key as the
images:

```sh
rm -rf installer-signed
chalkbuild sign --key keys/db.key --cert keys/db.crt installer installer-signed
```

An image signed with another key than the installer boots, but its first boot asks for the
recovery key, as [Sign the installer](../build/sign-images.md#sign-the-installer) explains.

Write the signed ISO to the whole stick, not to a partition of it:

```sh
sudo dd if=installer-signed/chalkos-installer.iso of=/dev/<usb-disk> \
  bs=4M conv=fsync status=progress
```

`<usb-disk>` is the stick's device, for example `sdb`; `lsblk` lists the candidates. The
command overwrites everything on the stick. With a BMC, mount
`installer-signed/chalkos-installer.iso` as virtual media instead.

Boot the machine from the stick through the firmware's boot menu. The screen shows the boot's
status and then the console. The first serial port, which a BMC's serial-over-LAN shows, gets the
kernel's and chalkd's lines.

## Note the address and the fingerprint

Once chalkd starts in maintenance mode, the machine's screen shows
[the console](console.md). Its header keeps the SHA-256 fingerprint of chalkd's self-signed
certificate and the machine's addresses on screen, and follows the addresses when they change:

```text
nixos  maintenance  up 2m
image 81bd81bd81bd
maintenance mode: chalkctl install installs this node
fingerprint <fingerprint>
addresses 192.168.1.50
```

`<fingerprint>` is 64 hex characters. Note it and the address. A reboot into maintenance mode
shows a new fingerprint. chalkd prints both on the serial console too, as
[A node in maintenance mode is trusted by its fingerprint](../how-it-works/node/security.md#a-node-in-maintenance-mode-is-trusted-by-its-fingerprint)
shows. When its first line there says `accepting any client`, the installer or image has no OS CA:
it is the generic installer, or the flake's `mkImages` has no `osCA`.

In maintenance mode the machine takes its addresses by DHCP, and by SLAAC where a router sends
advertisements. Such an address is not yet the one the inventory names, so the commands below
reach it with `--address`.

## Enrol the Secure Boot keys

Skip this step when the machine's firmware holds your db certificate already, or when the machine
runs without Secure Boot. A machine whose firmware is in Setup Mode boots the signed installer
without checking it and gets its keys from
[`chalkctl secureboot enroll`](../reference/cli/chalkctl_secureboot_enroll.md), at the address
and fingerprint from the console:

```sh
chalkctl secureboot enroll --keys keys --address=<address> --fingerprint=<fingerprint> \
  --secrets=secrets.age
```

The node writes the keys and reboots with Secure Boot on. It boots with a new certificate, so
chalkctl prints a new fingerprint: compare it with the console and use it from here on.
[Enrol the keys in Setup Mode](secure-boot.md#enrol-the-keys-in-setup-mode)
explains the checks, `--microsoft` and `--option-roms`, and what to do when the enrolment fails.

## Choose the system disk

The installer writes only to the node's system disk: `spec.disk` of the `SystemDisk` document in
the node's file. List the machine's disks with
[`chalkctl disks`](../reference/cli/chalkctl_disks.md), at the address and fingerprint from the
console:

```sh
chalkctl disks --address=<address> --fingerprint=<fingerprint> --secrets=secrets.age
```

`--secrets` gives chalkctl the certificate that the machine in maintenance mode asks for. The
output lists each disk with the properties a selector matches, as
[Find the disks](storage-volumes.md#find-the-disks) shows. The installer's own medium shows the use
`boot`.

Name the disk by a `/dev/disk/by-id/` path or a selector, because device names such as
`/dev/sda` can change between boots. Here `cp1` names its NVMe disk by its `by-id` path:

```yaml title="nodes/cp1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec:
  disk: {path: /dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001}
```

chalkctl reads the file as it is when a command runs. After a change, check it again with
`chalkctl validate cp1`.

## Install the node

Run [`chalkctl install`](../reference/cli/chalkctl_install.md) with the node's name, the address
and the fingerprint:

```sh
chalkctl install cp1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
```

When reading the console is impractical, `--insecure` accepts any certificate and prints
`chalkctl: the node's certificate fingerprint is <fingerprint>`. Compare it with the console
afterwards. chalkctl sends the node its config, certificates and secrets, with the Kubernetes layer
its [Kubernetes share](../how-it-works/kubernetes/overview.md#the-kubernetes-share-holds-the-secrets-of-a-node)
too. It refuses a node that is installed already, with
`cp1 at <address> is installed already`.

!!! note "With the Kubernetes layer, a revoked name is refused"

    chalkctl asks the other control planes of the inventory for the revoked nodes, and refuses a
    name that `chalkctl node revoke` revoked, with
    `cp3 is revoked since <time>, and install installs cp3 only once chalkctl node unrevoke cp3
    lets it back in, or with --ignore-revoked`.
    It also refuses while control planes were bootstrapped and none says which nodes are revoked.
    A control plane that does not answer as an installed node, such as one in maintenance mode or
    switched off, counts as not installed yet, so the cluster's first nodes install while the
    others wait. `--ignore-revoked` installs without asking.
    [Reuse a removed node's name](add-remove-nodes.md#reuse-a-removed-nodes-name) frees the name.

The installer gets the image as well. For an image in a registry it pulls the image itself, as the
end of this section describes. Otherwise chalkctl sends it the image, from the directory of the
node's image in the inventory or the one `--image` names. chalkctl refuses a directory whose raw image is not the build its
`metadata.json` names, and sends the store, hash tree, UKI and boot loader. The output below is
illustrative: its lines are chalkctl's, and the disk, the image hash and the size are an
example's.

```text
installing cp1 onto /dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001 from /home/alice/cluster/images/node-metal-signed: chalkos 5f1e60980b55, sending 297.3 MiB of store, hash tree, UKI and boot loader
cp1 is installed and reboots
chalkctl recovery-key cp1 prints the key that unlocks it when its TPM fails
```

The first line names the disk, the image directory, the image's ID `chalkos` and the image's name,
whose form [Status, logs and the console](status-and-logs.md#read-chalkctl-status-from-the-top)
explains.

When Secure Boot is enforced, the installer checks the UKI and the boot loader against the
machine's db and dbx, and refuses an unsigned or wrongly signed image. It writes the image to the
system disk, and the image checks the config with its own checker, as
[An image that does not run checks its own config](../how-it-works/node/config-pipeline.md#an-image-that-does-not-run-checks-its-own-config)
describes. Then the installer sets up STATE, VAR and the volumes, and adds a UEFI boot entry named
`chalkos`, as
[The install in code](../contribute/internals/install.md)
follows step by step. A refused config leaves the machine in maintenance mode, as
[If it fails](#if-it-fails) describes.

The boot entry goes first in the boot order, so the stick may stay in. Remove it before the next
install elsewhere. When the inventory names the node's image in a registry, the installer pulls it,
and the first line ends in `which the installer pulls from <reference>`.
[Install from the registry](../build/publish-images.md#install-from-the-registry) covers the
machine's route to the registry and its credentials.

The machine reboots into its image. The node then runs in normal mode with the network of its
config, and chalkctl reaches it at the address the inventory names. Keep that address the same as
the static address of the node's `Network`, because chalkctl never reads it from the node's
config.

## Continue an install that stopped

An install that stops halfway, because chalkctl was interrupted, the network dropped or the machine
lost power, continues where it stopped. Run the same `chalkctl install` command again, with the
fingerprint the console prints now. The installer logs `continuing the install on <disk>` on the
console and skips a slot that holds the image already. Until the boot loader is written, the disk
boots nothing.

An install with another image that lays the disk out the same way continues on it too, while
STATE holds neither an installed node nor the earlier image's config, as after a refused config.
The installer writes the new image over slot A and removes the earlier image's UKI, whatever its
image ID, as [Files on the ESP](../reference/image-layout.md#files-on-the-esp) describes. Once an
earlier attempt wrote the node's config to STATE, another image needs `--wipe-disk`.

## Replace what a disk holds

The installer takes a disk on which `blkid` finds no signature or only an empty partition table,
or one that an earlier attempt of this install left. It refuses any other disk and names what it
found:

```text
the target disk /dev/nvme0n1 (model "...", size ..., serial "...") carries data (dos); pass --wipe-disk to replace it
the target disk /dev/nvme0n1 (model "...", size ..., serial "...") is not one an earlier install of this image left: ...; pass --wipe-disk to replace it
the target disk /dev/nvme0n1 (model "...", size ..., serial "...") holds an installed node; pass --wipe-disk to replace it
slot A of the target disk /dev/nvme0n1 (model "...", size ..., serial "...") holds the store 3df89200aef8615c of another image, whose config STATE holds; pass --wipe-disk to replace it
```

!!! danger "--wipe-disk destroys the disk's data"

    With `--wipe-disk`, the installer replaces whatever the disk holds, an installed node
    included, and nothing on it can be recovered. Check the disk in `chalkctl disks` first. A
    disk on which `blkid` finds no signature counts as empty without the flag, even when it
    holds data. Once chalkctl printed the disk it installs onto, it asks for the node's name
    before the installer touches the disk. Without a terminal, as in a script, the command
    refuses unless `--yes` is given.

[Add and remove nodes](add-remove-nodes.md) covers reinstalling a node that is part of the
cluster.

## Keep the recovery keys at hand

Each node's encrypted volumes have a second keyslot, by default its recovery key, which unlocks
them when the TPM does not. [`chalkctl recovery-key`](../reference/cli/chalkctl_recovery-key.md)
derives the key from the secrets file:

```sh
chalkctl recovery-key cp1 --secrets=secrets.age
```

No file stores the keys, and installing never changes the secrets file, so a backup of the
secrets file backs up every recovery key. Keep the nodes' keys where you can read them at a node's console, such as in a password manager.
[The console asks for a passphrase at boot](recover-node.md#the-console-asks-for-a-passphrase-at-boot)
covers the prompt.

## Install the other nodes

Install the remaining nodes the same way. Boot each into maintenance mode, note the address and
the fingerprint, choose the disk and run `chalkctl install`.

!!! note "With the Kubernetes layer, only the first control plane needs a bootstrap"

    Once `cp1` is back up in normal mode, [Bootstrap and get a kubeconfig](../kubernetes/bootstrap.md)
    starts the cluster on it. The other nodes need no bootstrap. Workers join at the cluster
    endpoint with the kubelet certificate chalkctl gave them. Further control planes join etcd
    and the control plane on their own, as
    [Run a highly available control plane](../kubernetes/ha-control-plane.md) describes.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) shows the node's config, image, platform,
volumes, certificates and failed units, as
[Status, logs and the console](status-and-logs.md) explains:

```sh
chalkctl status cp1
```

The node shows its volumes as mounted and no failed units.

## If it fails

A config that the image's checker refuses ends `chalkctl install` with the image's message, such as
that of the validator `hardware-rack` from
[Validate config with a program](../build/validate-config.md), on the node `cp1`:

```text
chalkctl: install cp1: failed_precondition: install: the config is refused: Hardware/hardware: metadata.name: a node with a Hardware document needs a Rack document, which says where the server is mounted; the target disk boots nothing, and an install of the image continues on it
```

The machine stays in maintenance mode. Fix the config and run the same `chalkctl install` again: the
install continues on the disk without `--wipe-disk`, also with an image rebuilt with a fixed
validator. Build the installer from the same chalkos release as the image, so that both lay out
the storage the same way. Otherwise the installer refuses the config once the image's store is on
the disk, names the parts of the storage that differ, and leaves a disk that boots nothing. Then
install with the installer built with the image, with `--wipe-disk` if it refuses the disk's
layout.

chalkctl reports the installer's other refusals as its error. The common errors and symptoms:

| Message or symptom | Cause and fix |
| --- | --- |
| `the maintenance image trusts another OS CA than the secrets file, so it refuses chalkctl's certificate` | The installer or the image was built before the OS CA was rotated, or from another cluster's `secrets.pub.json`. Build and sign both again from the current one. |
| `Secure Boot would refuse the UKI: ...` or `Secure Boot would refuse the boot loader: ...` | The image is unsigned, or signed with a key whose certificate is not in this machine's db. Sign the build again with `chalkbuild sign` and the enrolled key, or enrol the key's certificate. |
| `under the new db, Secure Boot would refuse the drivers the firmware loaded ...` from `chalkctl secureboot enroll` | The machine's cards carry option ROMs that db would not allow under the new keys. Enrol their digests with `--option-roms` or Microsoft's CAs with `--microsoft`, as [Allow the cards' option ROMs](secure-boot.md#allow-the-cards-option-roms) explains. |
| `STATE on the target disk ... does not open on this machine, so it belongs to another node` | The disk was installed in another machine. Use `--wipe-disk` if its data may go. |
| The first boot asks for a recovery key | The installer and the image were signed with different keys, or Secure Boot changed between install and boot. Type the key that `chalkctl recovery-key cp1` prints. |

[The console asks for a passphrase at boot](recover-node.md#the-console-asks-for-a-passphrase-at-boot)
explains the recovery-key prompt and how to make it go away. [Troubleshooting](troubleshooting.md)
covers a node that does not come up after the reboot.

## What next

- [Add and remove nodes](add-remove-nodes.md) grows or shrinks the cluster later.
- [The install in code](../contribute/internals/install.md) follows
  an install step by step through chalkctl, chalkd and the disk.
- [Sign images for Secure Boot](../build/sign-images.md) covers the key's life and why it must
  not change for an installed node.

With the Kubernetes layer, continue at [Bootstrap and get a kubeconfig](../kubernetes/bootstrap.md).
