---
title: "chalkctl install"
description: "Install a node that waits in maintenance mode"
---

# chalkctl install

Install a node that waits in maintenance mode

## Synopsis

Installs a node that waits in maintenance mode: a machine that booted the installer, or a
node's image booted for the first time. The node serves a self-signed certificate in maintenance
mode, so chalkctl verifies it by the SHA-256 fingerprint the node prints on its console
(--fingerprint), or accepts any certificate with --insecure and prints the fingerprint it saw,
which can be checked against the console afterwards; the secrets go over a second connection
pinned to that fingerprint.

chalkctl sends the node its config, the documents of the files the inventory gives it, checked
against the schemas of the image it installs, a node certificate, the OS CA, the secret of its
encrypted volumes' second keyslot (its recovery key, or a password) and, on a Kubernetes node, its
Kubernetes share. A node that runs its image already installs in place. A node that runs the
installer gets the image as well: the image --image names, or the node's image in the inventory,
signed with chalkbuild sign when the machine enforces Secure Boot. An image from a directory
follows the header over the connection. An image in a registry the installer pulls itself, by the
digest chalkctl resolved, through the Mirror of the registry's host and with the Registry
documents of the config chalkctl sends, and it takes the image's schemas, image.json and partition
definitions from the image's contract layer. With Secure Boot on, the installer checks the UKI and
the boot loader against the firmware's db
and dbx. It writes the image to the disk the node's SystemDisk names: a disk on which blkid finds
no signature, or one holding an unfinished install of the node, which it continues; any other
disk only with --wipe-disk. The node reboots into the installed image. The node certificate
carries the node's name in the inventory, which stays the node's name until it is reinstalled.
The command needs the secrets file.

```
chalkctl install <node> [flags]
```

## Examples

```
  # Install cp1, comparing the certificate with the fingerprint on its console.
  chalkctl install cp1 --secrets secrets.age \
    --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08

  # Install w1 at an address of the network it booted into, with an image chalkbuild signed.
  chalkctl install w1 --secrets secrets.age --address 192.168.1.50 --insecure \
    --image ./worker-image
```

## Options

```
      --address string         address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --fingerprint string     SHA-256 fingerprint the node prints on its console in maintenance mode
  -h, --help                   help for install
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --image string           image to install when the node runs the installer: a raw image with repart-output.json and repart.d next to it, or an image directory, which chalkctl sends (default the node's image in the inventory: from its directory, or from a registry, which the installer pulls)
      --insecure               accept any certificate and print the node's fingerprint
      --inventory string       inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --password-file string   file holding the password of a node whose fallback is a password
      --secrets string         secrets file, encrypted with age or not, or - to read it from standard input
      --wipe-disk              let the installer replace whatever the target disk holds, including an installed node; without it, the installer continues an earlier install of the node or takes a disk on which blkid finds no signature, which counts as empty even when it holds data
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Install and operate chalkos clusters

