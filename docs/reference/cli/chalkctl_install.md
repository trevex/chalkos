---
title: "chalkctl install"
description: "Install a node in maintenance mode"
---

# chalkctl install

Install a node in maintenance mode

## Synopsis

Installs a node that waits in maintenance mode, as the installer and a role image booted
for the first time do. The node serves a self-signed certificate in maintenance mode, so chalkctl
verifies it by the SHA-256 fingerprint the node prints on its console (--fingerprint), or accepts
any certificate with --insecure and prints the fingerprint it saw, which can be checked against
the console afterwards; the secrets go over a second connection pinned to that fingerprint.

chalkctl sends the node its identity from the cluster definition, a node certificate, the OS CA,
the secret of its encrypted volumes' second keyslot (its recovery key, or a password) and, on a
Kubernetes role, its Kubernetes share. A node that runs its role image already installs in place.
A node that runs the installer gets the role image of its platform as well: --image, or the image
chalkctl builds from the flake, signed for Secure Boot with --sign-key and --sign-cert when
given. The installer writes it to the disk the node's identity names: a disk on which blkid finds
no signature, or one holding an unfinished install of the node's role, which it continues; any
other disk only with --wipe-disk. The node reboots into the installed image.

```
chalkctl install <node> [flags]
```

## Examples

```
  # Install cp1, comparing the certificate with the fingerprint on its console.
  chalkctl install cp1 --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08

  # Install w1 at an address of the network it booted into, with a prebuilt, signed image.
  chalkctl install w1 --endpoint 192.168.1.50 --insecure --image ./w1-image \
    --sign-key db.key --sign-cert db.crt
```

## Options

```
      --cluster string         cluster to use when the flake defines several
      --endpoint string        address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --fingerprint string     SHA-256 fingerprint the node prints on its console in maintenance mode
      --flake string           directory of the flake that defines the cluster (default ".")
  -h, --help                   help for install
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --image string           role image to install when the node runs the installer: a raw image with repart-output.json and repart.d next to it, or the directory nix build produces (default: build the node's role image)
      --insecure               accept any certificate and print the node's fingerprint
      --manifest string        read the cluster's manifest from this file instead of evaluating the flake
      --password-file string   file holding the password of a node whose fallback is a password
      --secrets string         secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
      --sign-cert string       PEM certificate of the Secure Boot db signer
      --sign-key string        PEM key of the Secure Boot db signer, to sign the image's UKI and boot loader
      --wipe-disk              let the installer replace whatever the target disk holds, including an installed node; without it, the installer continues an earlier install of the node's role or takes a disk on which blkid finds no signature, which counts as empty even when it holds data
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

