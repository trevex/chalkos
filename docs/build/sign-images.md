---
title: "Sign images for Secure Boot"
description: "Create Secure Boot keys, enrol them in the firmware, sign a copy of each image and the installer, and name the signed copy in the inventory"
---

# Sign images for Secure Boot

This page is for an image author whose machines enforce Secure Boot. Signing is optional: a node
without Secure Boot installs and runs the unsigned image that `nix build` writes. At the end you
have Secure Boot keys enrolled in the machines' firmware, a signed copy of the image in
`images/node-metal-signed`, an inventory that names it, and a signed installer.

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
- For a machine whose firmware is not in Setup Mode, access to its firmware setup, its BMC or the
  vendor's tools, to enrol the certificate.

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

Each machine's firmware must hold `db.crt` in db, with Secure Boot on, before the node is
installed. A change to db, KEK or dbx changes PCR 7, so on an installed node the next boot asks
for the recovery key.

### Firmware in Setup Mode: chalkctl secureboot enroll

Firmware in Setup Mode has no platform key and accepts new keys from the operating system it
boots. Some machines leave the factory that way; on others, clear the platform key, or all
Secure Boot keys, in the firmware setup. In Setup Mode the firmware boots any image, so boot the
signed installer, as [Install a node](../operate/install.md#boot-the-machine-into-maintenance-mode)
describes, and note its address and fingerprint. Then run
[`chalkctl secureboot enroll`](../reference/cli/chalkctl_secureboot_enroll.md):

```console
$ chalkctl secureboot enroll --keys keys --address 192.168.1.50 \
    --fingerprint <fingerprint> --secrets secrets.age
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
the firmware would refuse the option ROMs of the machine's cards, as [Option ROMs](#option-roms)
explains. Sign the installer with `keys/db.key` first. `--force` enrols the keys all the same.

The node makes a new maintenance certificate when it boots again, and chalkctl reads it without a
fingerprint to verify it against. Compare the new fingerprint that chalkctl prints with the one on
the console, and pass it to `chalkctl install`.

Add `--microsoft` to enrol Microsoft's certificates as well: its UEFI CAs of 2011 and 2023 and its
option ROM CA of 2023 in db, and its KEK CAs of 2011 and 2023 in KEK. The firmware of many network,
storage and graphics cards, its option ROM, is signed by Microsoft. A machine whose db lacks
Microsoft's CA may lose its display in the firmware or fail to boot from such a card. When the
firmware setup offers to clear the keys and keep or restore the factory ones, the factory keys
usually include Microsoft's certificates already. Leave out `--microsoft` when the machine boots
only what you signed; [Option ROMs](#option-roms) explains how such a machine still loads the
firmware of its cards.

When a write fails, the node reports which one. db and KEK are written before PK, so the firmware
stays in Setup Mode until PK is written, and `chalkctl secureboot enroll` can run again. A machine
that refuses to boot the installer after the enrolment shows `Access Denied` or a security
violation: clear the keys in the firmware setup, check the signature of the installer, and enrol
again.

A machine that runs its image from its own disk instead of the installer, such as a VM, enrols the
same way. It deletes STATE, which holds nothing before the install and which the TPM seals to the
old PCR 7, and creates it again when it boots.

### Option ROMs

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
    --fingerprint <fingerprint> --secrets secrets.age
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

### Firmware not in Setup Mode

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
  [Install a node](../operate/install.md#boot-the-machine-into-maintenance-mode) shows how
  for QEMU, libvirt and Proxmox.

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

Boot a machine from the signed installer with Secure Boot on. Firmware refuses an image that no db
certificate signed, typically with `Access Denied` or a security violation message, so a boot
proves the enrolment. `chalkctl secureboot enroll` waits until the node reports Secure Boot on;
after an enrolment in another way, confirm in the firmware setup that Secure Boot is on and not in
Setup Mode, because in Setup Mode the firmware boots anything.

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
