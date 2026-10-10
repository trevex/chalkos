---
title: "os-release fields"
description: "The fields chalkos adds to /etc/os-release and the UKI's os-release, and what reads them"
---

# os-release fields

This page lists the os-release fields chalkos sets, their values and the checks that read them.
The `CHALKOS_*` fields come from `system.nixos.extraOSReleaseArgs` in `modules/node/upgrade.nix`,
the image ID from `modules/node/base/appliance.nix` and `modules/installer/default.nix`; they are
read through `pkg/uki/uki.go` and `pkg/chalkd/info.go`.

## Where os-release is

An image carries one os-release file in two places with the same content:

- `/etc/os-release` on the running node, in the read-only `/etc` the image assembles at boot;
- the `.osrel` section of the image's [UKI](glossary.md#uki), next to the kernel command line
  that names the store's [root hash](glossary.md#root-hash).

The UKI's Secure Boot signature covers its `.osrel` section, so on a node with
[Secure Boot](glossary.md#secure-boot) enforced, an image cannot claim another cluster,
[role](glossary.md#role), [platform](glossary.md#platform) or version than the one it was signed
with. chalkctl and chalkd read the UKI's copy of an image they are about to install, and
`/etc/os-release` for the image a node runs.

## Fields chalkos sets

| Field | Value | Source | Images |
| --- | --- | --- | --- |
| `IMAGE_ID` | `chalkos`; `chalkos-installer` on the [installer](glossary.md#installer) | `system.image.id` | every image |
| `IMAGE_VERSION` | the [image version](glossary.md#image-version), `0.1.0` unless a role sets it | `system.image.version` | every image |
| `CHALKOS_CLUSTER` | the cluster's name | [`chalkos.cluster.name`](options.md#chalkosclustername) | every image |
| `CHALKOS_ROLE` | the role's name | [`chalkos.role.name`](options.md#chalkosrolename) | role images; not the installer |
| `CHALKOS_PLATFORM` | the platform's name, such as `metal` or `kvm` | [`chalkos.platform.name`](options.md#chalkosplatformname) | role images; not the installer |
| `CHALKOS_BOOT_TRIES` | a positive integer, `3` by default | [`chalkos.upgrade.bootTries`](options.md#chalkosupgradeboottries) | every image |

`IMAGE_VERSION` is limited to 1 to 23 characters of `a-z`, `0-9`, `.`, `~`, `^` and `-`,
starting with a letter or a digit, because it becomes part of GPT labels and of systemd-boot's
entry IDs; evaluation fails on another value. A platform's name matches
`[a-z0-9][a-z0-9-]*`.

The other fields are NixOS's, unchanged: `ID=nixos`, `NAME`, `VERSION`, `VERSION_ID`,
`BUILD_ID`, `PRETTY_NAME` and the URLs. chalkos does not read them.

The os-release of an image of the test cluster's `test` role on `metal`, as its UKI carries it:

```text
ANSI_COLOR="0;38;2;126;186;228"
BUG_REPORT_URL="https://github.com/NixOS/nixpkgs/issues"
BUILD_ID="26.11.20261003.a7868a7"
CHALKOS_BOOT_TRIES=3
CHALKOS_CLUSTER=chalklab
CHALKOS_PLATFORM=metal
CHALKOS_ROLE=test
CPE_NAME="cpe:/o:nixos:nixos:26.11"
DEFAULT_HOSTNAME=nixos
DOCUMENTATION_URL="https://nixos.org/learn.html"
HOME_URL="https://nixos.org/"
ID=nixos
IMAGE_ID=chalkos
IMAGE_VERSION="0.1.0"
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

The installer's carries `IMAGE_ID="chalkos-installer"`, `CHALKOS_CLUSTER` and
`CHALKOS_BOOT_TRIES`, and no `CHALKOS_ROLE` or `CHALKOS_PLATFORM`.

## What reads each field

| Field | Read by | Check |
| --- | --- | --- |
| `IMAGE_ID` | chalkd's [Upgrade](api.md#method-upgrade) | The new image's ID equals the running image's. |
| `IMAGE_ID` | chalkd's upgrade and install, on the UKI | The UKI's ID equals the image header's. The UKI's file name on the [ESP](glossary.md#esp) is `<IMAGE_ID>_<IMAGE_VERSION>`. |
| `IMAGE_ID` | chalkd's upgrade, on the ESP | Only UKIs of the running image's ID are removed or counted; other UKIs on the ESP stay. |
| `IMAGE_VERSION` | chalkctl, reading an image | The version matches the pattern above. |
| `IMAGE_VERSION` | chalkd's Upgrade | A new image of the running version and the same root hash is installed already; one of the running version with another root hash is refused. |
| `IMAGE_VERSION` | chalkd's upgrade and install, on the UKI | The UKI's version equals the image header's. |
| `CHALKOS_CLUSTER` | chalkctl, reading an image | The image is of the cluster the manifest names. |
| `CHALKOS_CLUSTER`, `CHALKOS_ROLE` | chalkd's [Install](api.md#method-install) in place and [ApplyIdentity](api.md#method-applyidentity) | The identity's cluster and role equal the running image's. |
| `CHALKOS_CLUSTER`, `CHALKOS_ROLE`, `CHALKOS_PLATFORM` | chalkd's Upgrade | The new image's equal the running image's; a running image without them takes no upgrade. |
| `CHALKOS_ROLE`, `CHALKOS_PLATFORM` | chalkctl install and upgrade | The image is of the node's role and platform in the cluster definition; an image without them is no role image and is refused. |
| `CHALKOS_PLATFORM` | chalkd's Install in place and ApplyIdentity | The identity's platform equals the running image's: changing a node's platform is a reinstall. |
| `CHALKOS_BOOT_TRIES` | chalkd's upgrade, on the UKI | A positive integer, which becomes the boot counter in the UKI's name, `+3` by default. An install writes no counter. |
| `IMAGE_ID`, `IMAGE_VERSION`, `CHALKOS_CLUSTER`, `CHALKOS_ROLE`, `CHALKOS_PLATFORM` | chalkd's [Info](api.md#method-info) | Reported as they are; empty on an image that sets none. |
| `CHALKOS_PLATFORM` | chalkd's [Status](api.md#method-status) | Reported, and shown by [`chalkctl status`](cli/chalkctl_status.md) beside the platform the cluster definition declares. |
| `IMAGE_VERSION` | chalkd's Status and the health check | The running image's version, in the boot status and in the record of a boot that was not found healthy. |

[The image](../concepts/image.md) explains what an image is and [Upgrades](../concepts/upgrades.md)
how the checks above keep an upgrade within its cluster, role and platform.
