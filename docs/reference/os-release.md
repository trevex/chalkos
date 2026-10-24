---
title: "os-release fields"
description: "The fields chalkos adds to /etc/os-release and the UKI's os-release, and what reads them"
---

# os-release fields

This page is for image authors and operators who read an image's os-release or a refusal that
names one of its fields. It lists the os-release fields chalkos sets, their values and the checks
that read them. [The image](../how-it-works/node/image.md) explains what an image is and
[Upgrades](../how-it-works/node/upgrades.md) how these checks keep an upgrade within its platform.

## Where os-release is

An image carries one os-release file in two places with the same content:

- `/etc/os-release` on the running node, in the read-only `/etc` the image assembles at boot;
- the `.osrel` section of the image's UKI, next to the kernel command line
  that names the store's root hash.

The UKI's Secure Boot signature covers its `.osrel` section, so on a node with
Secure Boot enforced, an image cannot claim another
variant or platform than the one it was signed
with. chalkctl and chalkd read the UKI's copy of an image they are
about to install, and `/etc/os-release` for the image a node runs.

## Fields chalkos sets

| Field | Value | Source | Images |
| --- | --- | --- | --- |
| `IMAGE_ID` | `chalkos`; `chalkos-installer` on the installer | `system.image.id` | every image |
| `IMAGE_VERSION` | empty | `modules/node/upgrade.nix`; `system.image.version` must stay unset | every image |
| `CHALKOS_VARIANT` | the name of the mkImages variant the image is built as, such as `node` | `chalkos.variant.name` | node images; not the installer |
| `CHALKOS_PLATFORM` | the platform's name, such as `metal` or `kvm` | `chalkos.platform.name` | node images; not the installer |
| `CHALKOS_BOOT_TRIES` | a positive integer, `3` by default | `chalkos.upgrade.bootTries` | every image |

[Image options](image-options.md) describes the options in the Source column.

`IMAGE_VERSION` is empty in every image, so the generation in the file name orders the UKIs, as
[Boot, health and rollback](../how-it-works/node/boot-and-rollback.md) explains. The build fails when
`system.image.version` is set, with the message
`chalkos orders boot entries by generation; pass revision and tag to mkImages for provenance`. The
image ID matches `[a-z0-9._-]+`: it starts the UKIs' file names, systemd-boot lowercases only
the IDs of entries without a tries counter, and a `+` starts the counter. A platform's name
matches `[a-z0-9][a-z0-9-]*`.

The other fields are NixOS's, unchanged: `ID=nixos`, `NAME`, `VERSION`, `VERSION_ID`,
`BUILD_ID`, `PRETTY_NAME` and the URLs. chalkos does not read them.

The os-release of the `lab` template's image for `kvm`, as its UKI carries it:

```text
ANSI_COLOR="0;38;2;126;186;228"
BUG_REPORT_URL="https://github.com/NixOS/nixpkgs/issues"
BUILD_ID="26.11.20261003.a7868a7"
CHALKOS_BOOT_TRIES=3
CHALKOS_PLATFORM=kvm
CHALKOS_VARIANT=node
CPE_NAME="cpe:/o:nixos:nixos:26.11"
DEFAULT_HOSTNAME=nixos
DOCUMENTATION_URL="https://nixos.org/learn.html"
HOME_URL="https://nixos.org/"
ID=nixos
IMAGE_ID=chalkos
IMAGE_VERSION=""
LOGO="nix-snowflake"
NAME=NixOS
PRETTY_NAME="NixOS 26.11 (Zokor)"
SUPPORT_URL="https://nixos.org/community.html"
VENDOR_NAME=NixOS
VENDOR_URL="https://nixos.org/"
VERSION="26.11 (Zokor)"
VERSION_CODENAME=zokor
VERSION_ID="26.11"
```

The installer's carries `IMAGE_ID="chalkos-installer"` and `CHALKOS_BOOT_TRIES`, and no
`CHALKOS_VARIANT` or `CHALKOS_PLATFORM`.

## What reads each field

| Field | Read by | Check |
| --- | --- | --- |
| `IMAGE_ID` | chalkd's Upgrade | The new image's ID equals the running image's. |
| `IMAGE_ID` | chalkd's upgrade and install, on the UKI | The UKI's ID equals the image header's. The UKI's file name on the ESP is `<IMAGE_ID>_<generation>.efi`. |
| `IMAGE_ID` | chalkd's upgrade, on the ESP | Only UKIs of the running image's ID, and files that are no UKI whose names give a generation of it, are removed; other UKIs on the ESP stay. Every name `<IMAGE_ID>_<n>.efi` counts for the highest generation, whatever its UKI holds. |
| `IMAGE_VERSION` | chalkctl, reading an image | Set and empty; an image without it or with a value is refused. |
| `IMAGE_VERSION` | chalkd's upgrade and install, on the UKI | Set and empty, as for chalkctl. |
| `CHALKOS_PLATFORM` | chalkd's Upgrade | The new image's equals the running image's; a running image without it takes no upgrade. |
| `CHALKOS_PLATFORM` | chalkd's upgrade and install, on the UKI | The UKI's platform equals the image header's. |
| `CHALKOS_VARIANT`, `CHALKOS_PLATFORM` | chalkctl, reading an image | Both are set; an image without them is no node image and is refused. |
| `CHALKOS_PLATFORM` | chalkctl install and upgrade | The image is built for the platform of the image the node runs, or, on the installer, of the node's image in the inventory, as its directory's `metadata.json` gives it. |
| `CHALKOS_PLATFORM` | chalkd's Install in place and ApplyConfig | The config is translated for the running image's platform: changing a node's platform is a reinstall. |
| `CHALKOS_VARIANT`, `CHALKOS_PLATFORM` | chalkd's Contract | Reported as they are, with the image's schemas and `image.json`. chalkctl's install in place, `apply-config` and `storage reset` refuse a node whose image is built for another platform than its image in the inventory. |
| `CHALKOS_BOOT_TRIES` | chalkd's upgrade, on the UKI | A positive integer, which becomes the boot counter in the UKI's name, `+3` by default. An install writes no counter. |
| `IMAGE_ID`, `CHALKOS_VARIANT`, `CHALKOS_PLATFORM` | chalkd's Info | Reported as they are; empty on an image that sets none. |
| `CHALKOS_PLATFORM` | chalkd's Status | Reported, and shown by `chalkctl status` beside the platform of the node's image in the inventory. |

No check compares `CHALKOS_VARIANT`: a node may change its variant through an upgrade, and the
check of its config against the new image's schemas decides whether the image suits it.

No check reads the image's version, because it has none: chalkd's Info and Status name the running
image by the root hash on the kernel command line, and the record of a
boot that was not found healthy by that root hash and the entry's generation.

[Node API](api.md) describes the methods in the Read by column.

Sources: `system.nixos.extraOSReleaseArgs` in `modules/node/upgrade.nix` for the `CHALKOS_*`
fields and the empty `IMAGE_VERSION`, `modules/node/base/appliance.nix` and
`modules/installer/default.nix` for the image ID, and `pkg/uki/uki.go` and `pkg/chalkd/info.go`
for the checks that read them.
