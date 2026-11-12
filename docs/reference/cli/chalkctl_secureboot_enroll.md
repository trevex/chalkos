---
title: "chalkctl secureboot enroll"
description: "Enrol Secure Boot keys in the firmware of a node in maintenance mode whose firmware is in Setup Mode"
---

# chalkctl secureboot enroll

```sh
chalkctl secureboot enroll [flags]
```

Enrols the Secure Boot keys of --keys in the firmware of a node in maintenance mode, which must be
in Setup Mode, as firmware is once its platform key is cleared in its setup. chalkctl signs three
updates of the firmware's variables: db and KEK, appended to what the firmware holds, and PK, which
ends Setup Mode. It reads PK.key, PK.crt, KEK.key, KEK.crt, db.crt and owner.txt from --keys;
db.key, which signs images, may stay on the machine that signs them. With --microsoft, db also
gets Microsoft's UEFI CAs of 2011 and 2023 and its option ROM CA of 2023, and KEK Microsoft's KEK
CAs of 2011 and 2023; firmware needs them to start the option ROMs of many graphics and network
cards and boot loaders Microsoft signed.

The node refuses the keys unless the boot loader and UKI it booted verify against db with the new
keys, so sign the installer or image with the db key first (chalkbuild sign) and boot it; --force
enrols the keys all the same. A node that runs its image from its own disk deletes STATE, which
holds nothing before the install and which the TPM no longer unseals once the keys change, and
creates it again when it boots. The node writes PK last: when a write fails before it, the firmware
stays in Setup Mode and enroll can run again.

The node then reboots, and chalkctl waits until it answers with Secure Boot on. It makes a new
certificate in maintenance mode when it boots, which chalkctl accepts without verifying and prints
the fingerprint of; compare it with the node's console before the install. Like chalkctl disks, the
command reaches the node by --address, with --fingerprint or --insecure, and with the secrets file
or an admin's client file when its image carries an OS CA.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | required | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | — | Client file of an admin to authenticate with instead of the secrets file. |
| `--fingerprint`{ #flag-fingerprint } | string | — | SHA-256 fingerprint the node prints on its console in maintenance mode. |
| `--force`{ #flag-force } | boolean | `false` | Enrol the keys even if Secure Boot would refuse the boot loader or UKI the node booted under them. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--insecure`{ #flag-insecure } | boolean | `false` | Accept any certificate and print the node's fingerprint. |
| `--keys`{ #flag-keys } | string | required | Directory of the keys, as chalkctl secureboot new writes it. |
| `--microsoft`{ #flag-microsoft } | boolean | `false` | Also enrol Microsoft's KEK CAs and its UEFI and option ROM CAs, which sign the firmware of graphics and network cards and other boot loaders. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--timeout`{ #flag-timeout } | duration | `10m0s` | How long to wait for the node to boot again with Secure Boot on. |

## Examples

```sh
# Enrol the keys in a machine that booted the signed installer, then install it.
chalkctl secureboot enroll --keys keys --address 192.168.1.50 --insecure --secrets secrets.age
chalkctl install w1 --secrets secrets.age --address 192.168.1.50 --fingerprint <fingerprint>

# Enrol Microsoft's CAs too, for a graphics card's option ROM.
chalkctl secureboot enroll --keys keys --microsoft --address 192.168.1.50 \
  --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
```

## See also

- [chalkctl secureboot](chalkctl_secureboot.md): Create Secure Boot keys and enrol them in a node's firmware
