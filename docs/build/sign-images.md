---
title: "Sign images for Secure Boot"
description: "Create Secure Boot keys, sign a copy of each image and the installer, and name the signed copy in the inventory"
---

# Sign images for Secure Boot

This page is for an image author whose machines enforce Secure Boot. Signing is optional: a node
without Secure Boot installs and runs the unsigned image that `nix build` writes. At the end you
have Secure Boot keys, a signed copy of the image in `images/node-metal-signed`, an inventory
that names it, and a signed installer. [Enrol Secure Boot keys](../operate/secure-boot.md) puts
the keys into the machines' firmware.

Firmware with Secure Boot on starts a boot binary only when a certificate in its db signed it.
chalkos signs two binaries per image, systemd-boot and the UKI. The UKI's signature covers the
whole read-only store, through the root hash on its command line. Nix builds images unsigned, and
chalkbuild signs a copy on the machine that holds the key, so the key never enters `/nix/store`
or a binary cache, and nodes never hold it.

## When images need a signature

Sign the images when a machine enforces Secure Boot: Secure Boot is on, and the firmware is not
in Setup Mode. chalkd checks signatures only then.

A node without Secure Boot needs no signature, and its disks lose the protection Secure Boot gives.
Its disk keys are sealed to the Secure-Boot-off value of PCR 7, so the TPM unseals them for any
image the machine boots. Anyone allowed to upgrade the node, the operator role included, can then
install an image of their own and run it as root with the disks unlocked. Turning Secure Boot on
later changes PCR 7, so each node asks for its recovery key at every boot until it is reinstalled.

## Before you begin

You need:

- An image directory, as [Your first image flake](first-image.md) builds it into
  `images/node-metal`.
- chalkctl and chalkbuild. chalkbuild comes from `nix shell github:trevex/chalkos#chalkbuild`, or
  from `chalkos.packages.${system}.chalkbuild` in the cluster flake's dev shell.

Plan the keys once for the cluster's lifetime.
[The key stays the same for an installed node](#the-key-stays-the-same-for-an-installed-node)
explains why.

## Create the keys

Create the keys with [`chalkctl secureboot new`](../reference/cli/chalkctl_secureboot_new.md):

```console
$ chalkctl secureboot new keys --name <cluster>
wrote Secure Boot keys to keys: PK, KEK and db, each a .key and a .crt, and owner.txt
sign images with: chalkbuild sign --key keys/db.key --cert keys/db.crt <in> <out>
keep keys offline and backed up: whoever holds PK.key or KEK.key controls which images this firmware boots
```

Replace `<cluster>` with the cluster's name; the certificates are named `<cluster> Secure Boot
PK`, `... KEK` and `... db`. The directory holds three key pairs and the GUID that owns their
entries in the firmware:

| File | What it is for |
| --- | --- |
| `PK.key`, `PK.crt` | The platform key, which owns the firmware's keys and signs changes of KEK. |
| `KEK.key`, `KEK.crt` | The key exchange key, which signs changes of db and dbx. |
| `db.key`, `db.crt` | The db key, which signs the boot loader and the UKIs. |
| `owner.txt` | The GUID that owns the entries `chalkctl secureboot enroll` writes. |

Each choice of the keys has a reason:

- RSA, because chalkd refuses a signer with another key type, as many firmwares verify RSA
  signatures alone. RSA 2048 is the size the UEFI specification requires firmware to support,
  and some firmwares refuse larger keys.
- SHA-256, because chalkd refuses a chain with a certificate signed with SHA-1 or MD5.
- The validity, 20 years, does not affect the boot. Firmware has no trusted clock and does not
  check it, and neither does chalkd.

You can also bring keys of your own, made with openssl, as long as they are RSA 2048 keys with
SHA-256 certificates. The certificate in db may also be a CA that issued the signing certificate,
because chalkd accepts a signer that a db certificate issued. A CA lets you keep its key offline
and sign with a short-lived one, and adds a key to manage. `chalkctl secureboot enroll` reads a
directory with the names above; write a random GUID to `owner.txt`, for example with `uuidgen`.

The certificates are public. Commit them to the cluster's repository. The keys are not:
`db.key` lets anyone sign a boot image that every machine of the cluster accepts, and any image
signed with it unlocks the nodes' disks. `PK.key` and `KEK.key` let anyone change which
certificates the firmware trusts. Keep the keys out of the repository and out of `/nix/store`: in a
secrets manager, or on the machine that signs, readable by you alone, and keep a backup offline.

Decide who signs. Whoever holds `db.key` runs `chalkbuild sign` on a build machine and hands out
the signed image directories, so the key stays in one place. The people who install and upgrade
nodes need no key. chalkctl sends the signed directories as they are, and each node checks the
signatures against its firmware's db and dbx and refuses an image the key did not sign.

## Enrol the keys in the firmware

Enrol the keys in each machine's firmware before you install the node on it, with
`chalkctl secureboot enroll` or through the firmware setup or the BMC, as
[Enrol Secure Boot keys](../operate/secure-boot.md) describes.

## Sign the image

Build the image as before, then sign a copy of it next to the build:

```sh
nix build .#node-metal -o images/node-metal
rm -rf images/node-metal-signed
chalkbuild sign --key keys/db.key --cert keys/db.crt images/node-metal images/node-metal-signed
```

[`chalkbuild sign`](../reference/cli/chalkbuild_sign.md) copies the image directory and signs the
copy's boot loader, `/EFI/BOOT/BOOT*.EFI`, and UKIs, `/EFI/Linux/*.efi`, on the ESP of its raw
image. No other file on the ESP gains the signature. The copy holds no link into `/nix/store`,
and the build stays as it is.

The output directory must not exist, so the `rm -rf` removes the copy of the previous build.
When signing fails, or Ctrl+C stops it, chalkbuild removes the output directory again, so a
partly signed copy never stays behind.

## Point the inventory at the signed copy

Name the signed directory as the image's `source` in the inventory:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    router-metal: {source: {directory: images/router-metal-signed}}
    node-metal: {source: {directory: images/node-metal-signed}}
  groups:
    router: {imageName: router-metal, files: [router/*.yaml]}
    nodes: {imageName: node-metal, files: [cluster/*.yaml, bgp/fabric.yaml, bgp/secrets.yaml]}
  nodes:
    router: {address: 192.168.1.2, groups: [router]}
    node1: {address: "fdc8:a1b2:c3d4:ff::11", groups: [nodes], files: [nodes/node1.yaml]}
    node2: {address: "fdc8:a1b2:c3d4:ff::12", groups: [nodes], files: [nodes/node2.yaml]}
    node3: {address: "fdc8:a1b2:c3d4:ff::13", groups: [nodes], files: [nodes/node3.yaml]}
```

Signing leaves the store and its root hash as they are, so `chalkctl status` compares a node with
the same image as before. [`chalkctl install`](../reference/cli/chalkctl_install.md) and
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) take the signed directory the
inventory names, and need no key. After the next build, sign again before you upgrade: the
inventory names the copy, which a new build does not replace.

An upgrade replaces the UKI and the store and keeps the boot loader the node was installed with,
so systemd-boot stays signed by the key it was installed with.

## Sign the installer

The installer's directory holds a raw image and an ISO, and `chalkbuild sign` signs both in the
copy. Write `installer-signed/chalkos-installer.iso` to a USB stick or mount it as virtual media:

```sh
nix build .#installer -o installer
rm -rf installer-signed
chalkbuild sign --key keys/db.key --cert keys/db.crt installer installer-signed
```

Sign the installer with the key that signs the images. The installer seals each node's STATE to the
value PCR 7 has while the installer runs. An image signed with another key boots with another PCR
7, so its first boot asks for the recovery key.

## Sign an OCI layout

`chalkbuild sign` also signs an OCI image layout with the same key, for an image the nodes pull
from a registry. [Publish images to a registry](publish-images.md#sign-the-layout) builds the
layout and signs it.

## chalkbuild and the node check the signatures

`chalkbuild sign` checks the UKI and the boot loader of each image it signed against `--cert`, as
firmware with that certificate in db would. A wrong key or certificate therefore fails on the
machine that signs.

chalkctl checks no signature. It sends the image as the directory holds it. When Secure Boot is
enforced, each node checks the UKI, and on an install the boot loader, against its firmware's db
and dbx before it writes them. An unsigned image, or one signed with a key whose certificate is
not in db, stops with `Secure Boot would refuse the UKI: ...` or
`Secure Boot would refuse the boot loader: ...`. `chalkctl install` and `chalkctl upgrade` print
it as the node's error.

## Sign images for a lab

A lab's VMs trust the lab's own keys, which chalklab creates and enrols in their firmware.
chalkbuild never holds those keys. [`chalklab sign`](../reference/cli/chalklab_sign.md) signs a
copy of an image directory with them, and `chalkctl upgrade --image` takes the copy:

```sh
rm -rf images/node-kvm-signed
chalklab sign images/node-kvm --out images/node-kvm-signed
chalkctl upgrade --image images/node-kvm-signed \
  --inventory ~/.local/state/chalklab/lab/chalkos.yaml --allow-downtime
```

`chalklab status` names the lab's db key and certificate. They are test keys of that lab alone.

## The key stays the same for an installed node

A node's STATE, VAR and encrypted volumes are sealed to PCR 7, which includes the db certificate
that verified the boot binaries. An image signed with the same key unlocks the disks without
anyone typing a key. An image signed with another key that db also trusts boots too, but the
console asks for the node's recovery key, as
[A changed PCR 7 sends the unlock to the console](../how-it-works/node/storage.md#a-changed-pcr-7-sends-the-unlock-to-the-console)
explains.

That has three consequences:

- Sign the installer and the node images with the same key, as
  [Sign the installer](#sign-the-installer) explains.
- Keep one key for the life of every installed node. chalkos has no command that seals the disk
  keys again to a new PCR 7 value, so a node upgraded to an image signed with a new key asks for
  its recovery key at every boot. Such a boot waits at the prompt, and the health check never
  judges it, so the upgrade does not roll back on its own. The node waits until someone enters
  the key or resets it, and each reset uses one of the image's boot tries.
  [Recover a node](../operate/recover-node.md) covers the way out.
- Firmware updates that change db or dbx, and turning Secure Boot off or on, change PCR 7 too.
  Plan them with the recovery keys at hand.

## Check that it worked

Boot the signed installer on a machine whose firmware holds the keys, as
[Enrol Secure Boot keys](../operate/secure-boot.md#check-that-it-worked) checks.

During an install, chalkd checks the UKI and the boot loader against the machine's db and dbx
before it writes them. An install that completes has passed that check, and the installed node
boots.

## What next

- [Publish images to a registry](publish-images.md) pushes a signed layout.
- [Test and roll out an image](test-and-roll-out.md) checks the config and upgrades the nodes.
- [Security model](../how-it-works/node/security.md) explains the chain from the db certificate
  to every block of the store.
- [Install a node](../operate/install.md) installs a node from the signed
  installer.
