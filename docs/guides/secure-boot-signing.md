---
title: "Sign images for Secure Boot"
description: "Create a db signing key, enrol it in firmware and sign images and the installer with it"
---

# Sign images for Secure Boot

A Secure Boot db key decides which images a cluster's machines boot. Firmware with
[Secure Boot](../reference/glossary.md#secure-boot) on starts a boot binary only when a
certificate in its [db](../reference/glossary.md#db-and-dbx) signed it. chalkos signs two
binaries per image, systemd-boot and the [UKI](../reference/glossary.md#uki), and the UKI's
signature covers the whole read-only store through the [root hash](../reference/glossary.md#root-hash)
on its command line. This guide creates the key and certificate, gets the firmware of your
machines to trust the certificate, and signs the installer and the role images with the key.
Nix builds images unsigned; you sign them on your workstation, so the key never enters the Nix
store or a binary cache, and nodes never hold it.

!!! note "chalkos does not enrol keys"

    Getting the certificate into each machine's db is a step you do with the firmware's own
    tools, described below. [`chalkos.secureBoot.enrollment`](../reference/options.md#chalkossecurebootenrollment)
    and [`chalkos.secureBoot.require`](../reference/options.md#chalkossecurebootrequire) are
    reserved for key enrolment by chalkd and have no effect yet. Only chalklab enrols keys, its
    own, in the firmware of its virtual machines.

## Before you begin

You need openssl, chalkctl, and access to the firmware setup, the BMC or the vendor's tools of
every machine. Plan the key once for the cluster's lifetime:
[why the key must not change](#why-the-key-must-not-change-for-an-installed-node) explains what
changing it costs.

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

`db.crt` is public; commit it to the flake. `db.key` lets anyone sign a boot image every machine
of the cluster accepts, and any image signed with it unlocks the nodes' disks. Keep it out of the
flake and out of the Nix store: in a secrets manager or on the machine that signs, readable by you
alone.

## Name the certificate in the cluster definition

Set [`chalkos.secureBoot.signerCertificate`](../reference/options.md#chalkossecurebootsignercertificate)
to the certificate:

```nix title="cluster.nix"
{
  chalkos.secureBoot.signerCertificate = ./db.crt;
}
```

[`chalkctl install`](../reference/cli/chalkctl_install.md) and
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) then check the UKI, and on an install
the boot loader, against it before they send anything, as firmware with that certificate in db
would. An image signed with another key, or not at all, stops with
`Secure Boot would refuse the image's UKI: ...` on your workstation instead of at a node. Without
the option, chalkctl checks against the certificate given with `--sign-cert`.

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
  libvirt](kvm-proxmox-libvirt.md#what-the-vm-needs) shows how for QEMU, libvirt and Proxmox.

Enrol the certificate before the node is installed. Changing db, KEK or dbx changes
[PCR 7](../reference/glossary.md#pcr-7), so doing it on an installed node makes its next boot
ask for the recovery key.

## Sign the installer and the images

Every binary that firmware or systemd-boot starts must be signed: the installer's, the
role image's when a machine boots it directly, and each new UKI an upgrade brings. chalkctl signs
in three places.

[`chalkctl sign`](../reference/cli/chalkctl_sign.md) signs a disk image in place: the boot loader
(`/EFI/BOOT/BOOT*.EFI`) and the UKIs (`/EFI/Linux/*.efi`) on its ESP, and nothing else, so no
other file on the ESP gains the cluster's signature. It needs the image's partition description,
`repart-output.json`, or `<name>.iso.json` for the installer's ISO. A build output in the Nix store
is read-only, so sign a copy:

```sh
cp --sparse=always result/chalkos-installer_0.1.0.iso installer.iso
chmod u+w installer.iso
chalkctl sign --image=installer.iso --repart-json=result/chalkos-installer_0.1.0.iso.json \
  --key=db.key --cert=db.crt
```

Sign the installer this way, and a role image that a VM boots directly. Ctrl-C during the signing
can leave an image partly signed; sign a fresh copy then.

`chalkctl install` with `--sign-key` and `--sign-cert` signs the role image a machine receives
from the installer. chalkctl copies the UKI and the boot loader off the image, signs the copies
and streams them with the store, so the image in the Nix store stays as it is:

```sh
chalkctl install <node> --fingerprint=<fingerprint> --sign-key=db.key --sign-cert=db.crt
```

`chalkctl upgrade` with the same flags signs the UKI of each image it rolls out:

```sh
chalkctl upgrade --sign-key=db.key --sign-cert=db.crt
```

An upgrade replaces the UKI and the store and keeps the boot loader the node was installed with,
so systemd-boot stays signed by the key it was installed with. A lab's images are signed with the
lab's own keys: `chalklab sign` signs a copy of an image, and the lab's db key and certificate,
which `chalklab status` names, work with `--sign-key` and `--sign-cert`.

## Why the key must not change for an installed node

A node's [STATE](../reference/glossary.md#state), [VAR](../reference/glossary.md#var) and
encrypted volumes are sealed to PCR 7, which measures the Secure Boot state and the db
certificate that verified the boot binaries. An image signed with the same key boots with the
same PCR 7, so upgrades unlock the disks without anyone typing a key. An image signed with
another key that db also trusts boots, but PCR 7 differs, the TPM does not unseal, and the
console asks for the node's [recovery key](../reference/glossary.md#recovery-key).

That has three consequences:

- Sign the installer and the role images with the same key. The installer seals STATE as PCR 7
  reads while the installer runs, so a role image signed with another key asks for the recovery
  key on its first boot.
- Keep one key for the life of every installed node. chalkos has no command that seals the disk
  keys again to a new PCR 7 value, so a node upgraded to an image signed with a new key asks for
  its recovery key at every boot. Such a boot waits at the prompt and is never judged by the
  [health check](../reference/glossary.md#health-check), so the upgrade does not roll back on its
  own: the node waits until someone enters the key or resets it, and each reset uses one of the
  image's boot tries. [Recover a node](recover-node.md) covers the way out.
- Firmware updates that change db or dbx, and turning Secure Boot off or on, change PCR 7 too.
  Plan them with the recovery keys at hand.

## Running without Secure Boot

chalkos installs and runs on machines with Secure Boot off, and the images need no signatures
there. chalkd checks signatures only when Secure Boot is enforced: on, and the firmware not in
Setup Mode. The cost is the protection itself. The disk keys are sealed to the Secure-Boot-off
value of PCR 7, so the TPM unseals them for any image the machine boots, and anyone allowed to
upgrade the node, the [operator](../reference/glossary.md#operator) role included, can install an
image of their own and run it as root with the disks unlocked. Turning Secure Boot on later
changes PCR 7, and each node asks for its recovery key once more.

## Check that it worked

A signed installer that boots with Secure Boot on proves the enrolment: firmware refuses an
image no db certificate signed, typically with `Access Denied` or a security violation message.
Confirm in the firmware setup that Secure Boot is on and not in Setup Mode, as in Setup Mode it
boots anything. During an install, chalkd checks the UKI and the boot loader against the
machine's db and dbx before it writes them; an install that completes has passed that check,
and the installed node boots.

## What next

- [Security model](../concepts/security.md) explains the chain from the db certificate to every
  block of the store, and what Secure Boot does for upgrades.
- [Install on bare metal](install-bare-metal.md) uses the key for the installer and the install.
- [The image](../concepts/image.md) describes what is signed and what the signature covers.
- [`chalkctl sign`](../reference/cli/chalkctl_sign.md) lists the command's flags.
