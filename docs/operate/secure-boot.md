---
title: "Enrol Secure Boot keys"
description: "Enrol the cluster's Secure Boot keys in a machine's firmware with chalkctl secureboot enroll, or through its setup screen or BMC"
---

# Enrol Secure Boot keys

This page is for an operator whose machines enforce Secure Boot. At the end each machine's
firmware holds the cluster's db certificate, with Secure Boot on, and boots the signed installer.
[Sign images for Secure Boot](../build/sign-images.md) creates the keys and signs the images and
the installer.

Each machine's firmware must hold `db.crt` in db, with Secure Boot on, before the node is
installed. A change to db, KEK or dbx changes PCR 7, so on an installed node the next boot asks
for the recovery key.

## Before you begin

You need:

- The keys directory that
  [`chalkctl secureboot new`](../reference/cli/chalkctl_secureboot_new.md) writes, as
  [Create the keys](../build/sign-images.md#create-the-keys) describes.
- The installer, signed with `keys/db.key`, as
  [Sign the installer](../build/sign-images.md#sign-the-installer) describes.
- For a machine whose firmware is not in Setup Mode, access to its firmware setup, its BMC or the
  vendor's tools, to enrol the certificate.

## Enrol the keys in Setup Mode

Firmware in Setup Mode has no platform key and accepts new keys from the operating system it
boots. Some machines leave the factory that way; on others, clear the platform key, or all
Secure Boot keys, in the firmware setup. In Setup Mode the firmware boots any image, so boot the
signed installer, as [Install a node](install.md#boot-the-machine-into-maintenance-mode)
describes, and note its address and fingerprint. Then run
[`chalkctl secureboot enroll`](../reference/cli/chalkctl_secureboot_enroll.md):

```console
$ chalkctl secureboot enroll --keys keys --address 192.168.1.50 \
    --fingerprint <fingerprint> --secrets=secrets.age
db before: empty
db after:
  CN=<cluster> Secure Boot db
KEK before: empty
KEK after:
  CN=<cluster> Secure Boot KEK
the node reboots with Secure Boot enforced
chalkctl: the node's certificate fingerprint is now <new fingerprint>; compare it with the node's console and install with --fingerprint <new fingerprint>
Secure Boot is on; install the node with chalkctl install
```

chalkctl signs the updates of db, KEK and PK on the workstation, so the keys never leave it, and
the node writes them: db and KEK are added to the entries the firmware holds, and PK comes last,
which ends Setup Mode. The node then reboots, and chalkctl waits until it answers with Secure
Boot on. Pick the installer in the firmware's boot menu if the machine boots something else.

The command reads `PK.key`, `PK.crt`, `KEK.key`, `KEK.crt`, `db.crt` and `owner.txt` from
`--keys`. It does not need `db.key`, so the db key can stay on the machine that signs images, and
the enrolment can run where PK and KEK are kept.

The node checks the keys before it writes them. It refuses firmware that is not in Setup Mode, and
refuses keys under which Secure Boot would refuse the boot loader or the UKI it booted, so a
machine never ends up with keys that do not start the installer. It also refuses keys under which
the firmware would refuse the option ROMs of the machine's cards, as
[Allow the cards' option ROMs](#allow-the-cards-option-roms) explains. Sign the installer with
`keys/db.key` first. `--force` enrols the keys all the same.

The node makes a new maintenance certificate when it boots again, and chalkctl reads it without a
fingerprint to verify it against. Compare the new fingerprint that chalkctl prints with the one on
the console, and pass it to `chalkctl install`.

Add `--microsoft` to enrol Microsoft's certificates as well: its UEFI CAs of 2011 and 2023 and its
option ROM CA of 2023 in db, and its KEK CAs of 2011 and 2023 in KEK. The firmware of many network,
storage and graphics cards, its option ROM, is signed by Microsoft. A machine whose db lacks
Microsoft's CA may lose its display in the firmware or fail to boot from such a card. When the
firmware setup offers to clear the keys and keep or restore the factory ones, the factory keys
usually include Microsoft's certificates already. Leave out `--microsoft` when the machine boots
only what you signed; [Allow the cards' option ROMs](#allow-the-cards-option-roms) explains how
such a machine still loads the firmware of its cards.

A machine that runs its image from its own disk instead of the installer, such as a VM, enrols the
same way. It deletes STATE, which holds nothing before the install and which the TPM seals to the
old PCR 7, and creates it again when it boots.

## Allow the cards' option ROMs

An option ROM is the firmware of an expansion card, such as a graphics card, a network card or a
disk controller, which the machine's firmware loads at every boot to show the screen, boot from
the network or reach the disks. Under Secure Boot the firmware loads an option ROM only when db
allows it: by a certificate that signed it, or by its SHA-256 digest. Most option ROMs are signed
by Microsoft. With only your own keys in db, the firmware refuses them, and the machine may show
nothing on a graphics card or no longer boot from a card.

The firmware records each option ROM it loads in the TPM event log, under PCR 2, with the digest
db must hold. Before it writes the keys, the node reads that log, refuses keys under which db would
not allow every option ROM it lists, and names the option ROMs it would refuse:

```text
chalkctl: enroll the keys: failed_precondition: under the new db, Secure Boot would refuse the drivers the firmware loaded from outside its own firmware, as a rule the option ROMs of graphics and network cards: sha256:3f1a... PciRoot(0x0)/Pci(0x3,0x0)/Offset(0x10a00,0x2f9ff); enrol their digests with --option-roms, or Microsoft's UEFI CAs with --microsoft; without them the machine may show nothing or boot from no network card or disk controller; pass --force to enrol the keys all the same, which also skips the check of the boot loader and UKI
```

The device path names the card's PCI slot. There are three ways to allow the option ROMs:

| Flag | What db gets | Trade-off |
| --- | --- | --- |
| `--option-roms` | The digests of the option ROMs the firmware loaded at this boot, as the node reads them from the event log. chalkctl prints them before it enrols them. | db allows those exact option ROMs and nothing else Microsoft signed. A firmware update of a card, or a new card, brings another digest, and the firmware refuses it until db holds that digest too, which takes the firmware setup, or clearing the keys and enrolling again. |
| `--microsoft` | Microsoft's UEFI CAs, of which the CA of 2011 and the Option ROM CA of 2023 sign most option ROMs. | db allows every option ROM and boot loader Microsoft signed, including shims that boot other operating systems. Cards and firmware updates keep working. |
| `--db-hash <sha256>` | A digest you give, 64 hex digits, with or without `sha256:` in front; repeat the flag for more. | For an option ROM the firmware did not load at this boot, such as that of a card you add later: take the digest `--option-roms` printed on a machine with the same card and firmware. |

With `--option-roms`, chalkctl lists the option ROMs before the enrolment, and db lists their
digests after it:

```console
$ chalkctl secureboot enroll --keys keys --option-roms --address 192.168.1.50 \
    --fingerprint <fingerprint> --secrets=secrets.age
option ROMs the firmware measured:
  sha256:<digest> PciRoot(0x0)/Pci(0x3,0x0)/Offset(0x10a00,0x2f9ff)
db before: empty
db after:
  CN=<cluster> Secure Boot db
  sha256:<digest>
...
```

The check passes when db, after the enrolment, holds the digest of every option ROM in the event
log, or Microsoft's UEFI CA of 2011 or its Option ROM UEFI CA of 2023. The CA of 2011 signs the
option ROMs Microsoft signed before 2023, and the Option ROM CA of 2023 those it signs since. The
node cannot tell from the event log who signed an option ROM, so it counts either CA as allowing
all of them. Microsoft's UEFI CA of 2023 does not count: it signs boot loaders alone. Firmware that
kept its factory keys when you cleared the platform key often has Microsoft's CAs in db already;
the check counts those too. An option ROM whose digest dbx lists is refused whatever db holds, and
the node says so.

The check runs only when the node can read the event log. A machine without a TPM has none, and a
TPM 1.2 or a TPM whose log holds no SHA-256 digests gives none the node can read. chalkctl then
prints why the node could not check the option ROMs, and `--option-roms` finds none to enrol. The
node refuses a log that is corrupt or cut short. `--force` skips the check, together with the
check of the boot loader and UKI.

The check can be wrong in two rare ways:

- An option ROM signed with a SHA-1 Authenticode digest is looked up by its SHA-1 digest in db,
  so the SHA-256 digest `--option-roms` enrols does not allow it. Use `--microsoft` for such a
  card.
- Some firmware that is not based on EDK2 measures its own drivers into PCR 2 as well. The node
  then refuses keys for drivers the firmware never checks against db. `--option-roms` enrols their
  digests too, which does no harm.

## Enrol the certificate without Setup Mode

For a machine you do not want to put into Setup Mode, append the certificate to db in another way
and keep the entries already there. Use the way your hardware offers:

- The firmware setup screen. Many firmwares have a Secure Boot key management page that appends
  a db entry from a file on a FAT-formatted USB stick. They usually expect the certificate in DER
  form: `openssl x509 -in keys/db.crt -outform DER -out db.cer`.
- The BMC. A BMC that implements the Redfish `SecureBootDatabase` resource takes a POST of
  `{"CertificateString": "<PEM>", "CertificateType": "PEM"}` to
  `/redfish/v1/Systems/<system>/SecureBoot/SecureBootDatabases/db/Certificates`. Vendors' BMC web
  interfaces offer the same, and a BMC's API scales to many machines.
- The vendor's tools, such as the server vendor's command-line utilities that write firmware
  settings from the running operating system.
- Virtual machines. Their UEFI variables are a file you prepare.
  [Create the VM](install-vm.md#create-the-vm) shows how for QEMU, libvirt and Proxmox.

## Check that it worked

Boot a machine from the signed installer with Secure Boot on. Firmware refuses an image that no db
certificate signed, typically with `Access Denied` or a security violation message, so a boot
proves the enrolment. `chalkctl secureboot enroll` waits until the node reports Secure Boot on;
after an enrolment in another way, confirm in the firmware setup that Secure Boot is on and not in
Setup Mode, because in Setup Mode the firmware boots anything.

## If it fails

When a write fails, the node reports which one. db and KEK are written before PK, so the firmware
stays in Setup Mode until PK is written, and `chalkctl secureboot enroll` can run again. A machine
that refuses to boot the installer after the enrolment shows `Access Denied` or a security
violation: clear the keys in the firmware setup, check the signature of the installer, and enrol
again.

A refusal that names option ROMs needs `--option-roms`, `--microsoft` or `--db-hash`, as
[Allow the cards' option ROMs](#allow-the-cards-option-roms) explains.

## What next

- [Install a node](install.md) installs the machine from the signed installer.
- [Sign images for Secure Boot](../build/sign-images.md) signs each new build with the same key.
- [Security model](../how-it-works/node/security.md) explains what Secure Boot protects.
