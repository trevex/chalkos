---
title: "Sign images for Secure Boot"
description: "An optional step: create a db key, sign a copy of each image and the installer, and name the signed copy in the inventory"
---

# Sign images for Secure Boot

This page is for an image author whose machines enforce Secure Boot. Signing is optional: a node
without Secure Boot installs and runs the unsigned image that `nix build` writes. At the end you
have a db key and certificate, a signed copy of the image in `images/node-metal-signed`, an
inventory that names it, and a signed installer.

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
- openssl and chalkbuild. chalkbuild comes from `nix shell github:trevex/chalkos#chalkbuild`, or
  from `chalkos.packages.${system}.chalkbuild` in the cluster flake's dev shell.
- Access to the firmware setup, the BMC or the vendor's tools of every machine, to enrol the
  certificate.

Plan the key once for the cluster's lifetime.
[The key stays the same for an installed node](#the-key-stays-the-same-for-an-installed-node)
explains why.

## Create the db key and certificate

Create an RSA key and a self-signed certificate for it:

```sh
openssl req -new -x509 -newkey rsa:2048 -nodes -sha256 -days 3650 \
  -subj "/CN=<cluster> Secure Boot db/" -keyout db.key -out db.crt
```

Replace `<cluster>` with the cluster's name. Each choice in the command has a reason:

- RSA, because chalkd refuses a signer with another key type, as many firmwares verify RSA
  signatures alone. RSA 2048 is the size the UEFI specification requires firmware to support,
  and some firmwares refuse larger keys.
- SHA-256, because chalkd refuses a chain with a certificate signed with SHA-1 or MD5.
- The validity does not affect the boot. Firmware has no trusted clock and does not check it, and
  neither does chalkd. Pick one longer than the cluster's expected life.

The certificate in db may also be a CA that issued the signing certificate, because chalkd
accepts a signer that a db certificate issued. A CA lets you keep its key offline and sign with a
short-lived one, and adds a key to manage.

`db.crt` is public. Commit it to the cluster's repository. `db.key` lets anyone sign a boot image
that every machine of the cluster accepts, and any image signed with it unlocks the nodes' disks.
Keep it out of the repository and out of `/nix/store`: in a secrets manager, or on the machine
that signs, readable by you alone.

## Enrol the certificate in the firmware

chalkos does not enrol keys today. Each machine's firmware must hold `db.crt` in db, with Secure
Boot on, before it boots the signed installer or image. The
[roadmap](../contribute/roadmap.md#secure-boot-key-enrolment) describes the planned enrolment.

Append the certificate to db and keep the entries already there. Microsoft's UEFI CA signs the
option ROMs of many network, storage and graphics cards, and a machine whose db lacks it may lose
its display in the firmware or fail to boot from such a card. Use the way your hardware offers:

- The firmware setup screen. Many firmwares have a Secure Boot key management page that appends
  a db entry from a file on a FAT-formatted USB stick. They usually expect the certificate in DER
  form: `openssl x509 -in db.crt -outform DER -out db.cer`.
- The BMC. A BMC that implements the Redfish `SecureBootDatabase` resource takes a POST of
  `{"CertificateString": "<PEM>", "CertificateType": "PEM"}` to
  `/redfish/v1/Systems/<system>/SecureBoot/SecureBootDatabases/db/Certificates`. Vendors' BMC web
  interfaces offer the same, and a BMC's API scales to many machines.
- The vendor's tools, such as the server vendor's command-line utilities that write firmware
  settings from the running operating system.
- Setup Mode. After you clear the platform key in the firmware setup, the machine accepts new
  Secure Boot variables from any operating system, for example with efitools' `efi-updatevar` or
  sbctl from a live Linux. Writing your own platform key and KEK this way makes you the owner of
  the machine's keys. Keep Microsoft's KEK and db entries unless the machine needs none of them.
- Virtual machines. Their UEFI variables are a file you prepare.
  [Run on KVM, Proxmox or libvirt](../operate/kvm-proxmox-libvirt.md#what-the-vm-needs) shows how
  for QEMU, libvirt and Proxmox.

Enrol the certificate before the node is installed. A change to db, KEK or dbx changes PCR 7, so
on an installed node the next boot asks for the recovery key.

## Sign the image

Build the image as before, then sign a copy of it next to the build:

```sh
nix build .#node-metal -o images/node-metal
rm -rf images/node-metal-signed
chalkbuild sign --key db.key --cert db.crt images/node-metal images/node-metal-signed
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
    node-metal: {source: {directory: images/node-metal-signed}}
  all: {imageName: node-metal, files: [cluster/*.yaml]}
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    w1: {address: 10.0.0.21, files: [nodes/w1.yaml]}
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
chalkbuild sign --key db.key --cert db.crt installer installer-signed
```

Sign the installer with the key that signs the images. The installer seals each node's STATE to the
value PCR 7 has while the installer runs. An image signed with another key boots with another PCR
7, so its first boot asks for the recovery key.

## Sign an OCI layout

`chalkbuild sign` also takes an OCI image layout, as `mkImages` builds it for a registry. It signs
copies of the layout's UKI and boot-loader layers, checks them against `--cert` and writes a new
layout with a new manifest. The store, hash tree and contract layers are copied as they are, and
the output directory must not exist:

```sh
nix build .#node-metal-oci -o oci/node-metal
rm -rf oci/node-metal-signed
chalkbuild sign --key db.key --cert db.crt oci/node-metal oci/node-metal-signed
```

The command prints the new manifest's digest.
[Publish images to a registry](publish-images.md) exposes the layout and pushes the signed copy.

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

A node's STATE, VAR and encrypted volumes are sealed to PCR 7. PCR 7 measures the Secure Boot
state and the db certificate that verified the boot binaries. An image signed with the same key
boots with the same PCR 7, so upgrades unlock the disks without anyone typing a key. An image
signed with another key that db also trusts boots too, but PCR 7 differs, the TPM does not
unseal, and the console asks for the node's recovery key.

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

Boot a machine from the signed installer with Secure Boot on. Firmware refuses an image that no db
certificate signed, typically with `Access Denied` or a security violation message, so a boot
proves the enrolment. Confirm in the firmware setup that Secure Boot is on and not in Setup Mode,
because in Setup Mode the firmware boots anything.

During an install, chalkd checks the UKI and the boot loader against the machine's db and dbx
before it writes them. An install that completes has passed that check, and the installed node
boots.

## What next

- [Publish images to a registry](publish-images.md) pushes a signed layout.
- [Test and roll out an image](test-and-roll-out.md) checks the config and upgrades the nodes.
- [Security model](../how-it-works/node/security.md) explains the chain from the db certificate
  to every block of the store.
- [Install on bare metal](../operate/install-bare-metal.md) installs a node from the signed
  installer.
