---
title: "Sign images for Secure Boot"
description: "Create a db signing key, enrol it in firmware and sign images and the installer with it"
---

# Sign images for Secure Boot

A Secure Boot db key decides which images a cluster's machines boot. Firmware with Secure
Boot on starts a boot binary only when a certificate in its
db signed it. chalkos signs two binaries per image,
systemd-boot and the UKI, and the UKI's signature covers the whole
read-only store through the root hash on its command line.
This guide creates the key and certificate, gets the firmware of your machines to trust the
certificate, and signs the installer and the node images with
the key. Nix builds images unsigned; you sign them with
chalkbuild on the machine that holds the key, so the key
never enters the Nix store or a binary cache, and nodes never hold it.

!!! note "chalkos does not enrol keys"

    Getting the certificate into each machine's db is a step you do with the firmware's own
    tools, described below. Only chalklab enrols keys, its
    own, in the firmware of its virtual machines.

## Before you begin

You need openssl, chalkbuild and access to the firmware setup, the BMC or the vendor's tools of
every machine. chalkbuild comes from `nix shell github:trevex/chalkos#chalkbuild`, or from
`chalkos.packages.${system}.chalkbuild` in the cluster flake's dev shell. Plan the key once for
the cluster's lifetime: [why the key must not change](#why-the-key-must-not-change-for-an-installed-node)
explains what changing it costs.

## Create the db key and certificate

Create an RSA key and a self-signed certificate for it:

```sh
openssl req -new -x509 -newkey rsa:2048 -nodes -sha256 -days 3650 \
  -subj "/CN=<cluster> Secure Boot db/" -keyout db.key -out db.crt
```

The choices matter:

- RSA, because chalkd refuses a signer with another key type, as many firmwares verify RSA
  signatures alone. RSA 2048 is the size the UEFI specification requires firmware to support;
  some firmwares refuse larger keys.
- SHA-256, because chalkd refuses a chain with a certificate signed with SHA-1 or MD5.
- The validity does not matter to the boot: firmware has no trusted clock and does not check it,
  and neither does chalkd's check. Pick one longer than the cluster's expected life.

The certificate in db may also be a CA that signed the signing certificate: chalkd accepts a
signer that a db certificate issued. A CA lets you keep its key offline and sign with a
short-lived one, at the cost of one more key to manage.

`db.crt` is public; commit it to the cluster's repository, from where you pass it to the `--cert`
flag of `chalkbuild sign`.
`db.key` lets anyone sign a boot image every machine of the cluster accepts, and any image signed
with it unlocks the nodes' disks. Keep it out of the repository and out of the Nix store: in a
secrets manager or on the machine that signs, readable by you alone.

## Enrol the certificate in the firmware

Each machine's firmware must hold `db.crt` in db, with Secure Boot on, before it boots the signed
installer or image. Keep the entries that are there: Microsoft's UEFI CA signs the option ROMs of
many network, storage and graphics cards, and a machine whose db lacks it may lose its display in
the firmware or fail to boot from such a card. Append your certificate to db; do not replace db.
Pick the way your hardware offers:

- The firmware setup screen. Many firmwares have a Secure Boot key management page that appends
  a db entry from a file on a FAT-formatted USB stick. They usually expect the certificate in DER
  form: `openssl x509 -in db.crt -outform DER -out db.cer`.
- The BMC. A BMC that implements the Redfish `SecureBootDatabase` resource takes a POST of
  `{"CertificateString": "<PEM>", "CertificateType": "PEM"}` to
  `/redfish/v1/Systems/<system>/SecureBoot/SecureBootDatabases/db/Certificates`. Vendors' BMC web
  interfaces offer the same, and it scales to many machines.
- The vendor's tools, such as the server vendor's command-line utilities that write firmware
  settings from the running operating system.
- Setup Mode. After clearing the platform key in the firmware setup, the machine accepts new
  Secure Boot variables from any operating system, for example with efitools' `efi-updatevar` or
  sbctl from a live Linux. Writing your own platform key and KEK this way makes you the owner of
  the machine's keys; keep Microsoft's KEK and db entries unless the machine needs none of them.
- Virtual machines. Their variable store is a file you prepare; [Run on KVM, Proxmox or
  libvirt](../operate/kvm-proxmox-libvirt.md#what-the-vm-needs) shows how for QEMU, libvirt and Proxmox.

Enrol the certificate before the node is installed. Changing db, KEK or dbx changes
PCR 7, so doing it on an installed node makes its next boot
ask for the recovery key.

## Sign the images

Every binary that firmware or systemd-boot starts must be signed: the installer's, a node
image's when a machine boots it directly, and each new UKI an upgrade brings.
[`chalkbuild sign`](../reference/cli/chalkbuild_sign.md) signs a copy of an image directory as
`nix build` links it: the boot loader (`/EFI/BOOT/BOOT*.EFI`) and the UKIs (`/EFI/Linux/*.efi`)
on the ESP of its raw image, and nothing else, so no other file on
the ESP gains the cluster's signature. The build in the Nix store stays as it is, and the copy
holds no link into the store.

Build each image into a directory of its own, and sign it into the directory the inventory names
for the image, here `images/node-metal` for `node-metal: {source: {directory: images/node-metal}}`
in the inventory's `spec.images`. The output directory must not exist, so remove the signed copy
of the previous build before signing a new one:

```sh
nix build .#node-metal -o build/node-metal
rm -rf images/node-metal
chalkbuild sign --key=db.key --cert=db.crt build/node-metal images/node-metal
```

When signing fails, or Ctrl+C stops it, chalkbuild removes the output directory again, so a
partly signed copy never stays behind.
[`chalkctl install`](../reference/cli/chalkctl_install.md) and
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) then take the signed directory the
inventory names, and need no key.

`chalkbuild sign` takes an OCI image layout as well, as `mkImages` builds it for publishing to a
registry. It signs copies of the layout's UKI and boot-loader layers and writes a new layout with
them, under the same rule that the output directory must not exist:

```sh
nix build .#node-metal-oci -o build/node-metal-oci
rm -rf oci/node-metal
chalkbuild sign --key=db.key --cert=db.crt build/node-metal-oci oci/node-metal
```

[Publish images to a registry](publish-images.md) pushes the signed layout and names it in the
inventory.

An upgrade replaces the UKI and the store and keeps the boot loader the node was installed with,
so systemd-boot stays signed by the key it was installed with.

## Sign the installer

The installer's directory holds a raw image and an ISO, and `chalkbuild sign` signs both in the
copy. Write `installer-signed/chalkos-installer.iso` to a USB stick or mount it as virtual media:

```sh
nix build .#installer -o installer
rm -rf installer-signed
chalkbuild sign --key=db.key --cert=db.crt installer installer-signed
```

## How are the signatures checked?

`chalkbuild sign` checks the UKI and the boot loader of each image it signed against `--cert`, as
firmware with that certificate in db would, so a wrong key or certificate fails on the machine
that signs.

chalkctl checks no signature; it sends the image as the directory holds it. Each node checks the
UKI, and on an install the boot loader, against its firmware's db and dbx before it writes them,
when Secure Boot is enforced. An unsigned image, or one signed with a key whose certificate is not
in db, stops with `Secure Boot would refuse the UKI: ...` or `... boot loader: ...`, which
`chalkctl install` or `chalkctl upgrade` prints as the node's error.

## Sign images for a lab

A lab's VMs trust the lab's own keys, which chalkbuild never
holds. [`chalklab sign`](../reference/cli/chalklab_sign.md) signs a copy of an image directory with
them, and `chalkctl upgrade --image` takes the copy:

```sh
rm -rf images/node-kvm-signed
chalklab sign images/node-kvm --out images/node-kvm-signed
chalkctl upgrade --image images/node-kvm-signed \
  --inventory ~/.local/state/chalklab/lab/chalkos.yaml --allow-downtime
```

## Why the key must not change for an installed node

A node's STATE, VAR and encrypted
volumes are sealed to PCR 7, which measures the Secure Boot state and the db certificate that
verified the boot binaries. An image signed with the same key boots with the same PCR 7, so upgrades
unlock the disks without anyone typing a key. An image signed with another key that db also trusts
boots, but PCR 7 differs, the TPM does not unseal, and the console
asks for the node's recovery key.

That has three consequences:

- Sign the installer and the node images with the same key. The installer seals STATE as PCR 7
  reads while the installer runs, so an image signed with another key asks for the recovery
  key on its first boot.
- Keep one key for the life of every installed node. chalkos has no command that seals the disk
  keys again to a new PCR 7 value, so a node upgraded to an image signed with a new key asks for
  its recovery key at every boot. Such a boot waits at the prompt and is never judged by the
  health check, so the upgrade does not roll back on its
  own: the node waits until someone enters the key or resets it, and each reset uses one of the
  image's boot tries. [Recover a node](../operate/recover-node.md) covers the way out.
- Firmware updates that change db or dbx, and turning Secure Boot off or on, change PCR 7 too.
  Plan them with the recovery keys at hand.

## Running without Secure Boot

chalkos installs and runs on machines with Secure Boot off, and the images need no signatures
there. chalkd checks signatures only when Secure Boot is enforced: on, and the firmware not in
Setup Mode. The cost is the protection itself. The disk keys are sealed to the Secure-Boot-off
value of PCR 7, so the TPM unseals them for any image the machine boots, and anyone allowed to
upgrade the node, the operator role included, can install an
image of their own and run it as root with the disks unlocked. Turning Secure Boot on later
changes PCR 7, so each node asks for its recovery key at every boot until it is reinstalled.

## Check that it worked

A signed installer that boots with Secure Boot on proves the enrolment: firmware refuses an
image no db certificate signed, typically with `Access Denied` or a security violation message.
Confirm in the firmware setup that Secure Boot is on and not in Setup Mode, as in Setup Mode it
boots anything. During an install, chalkd checks the UKI and the boot loader against the
machine's db and dbx before it writes them; an install that completes has passed that check,
and the installed node boots.

## What next

- [Security model](../how-it-works/node/security.md) explains the chain from the db certificate to every
  block of the store, and what Secure Boot does for upgrades.
- [Install on bare metal](../operate/install-bare-metal.md) signs the installer and the image and installs
  a node.
- [The image](../how-it-works/node/image.md) describes what is signed and what the signature covers.
- [`chalkbuild sign`](../reference/cli/chalkbuild_sign.md) lists the command's flags.
