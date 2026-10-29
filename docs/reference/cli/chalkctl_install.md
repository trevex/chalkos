---
title: "chalkctl install"
description: "Install a node that waits in maintenance mode"
---

# chalkctl install

```sh
chalkctl install <node> [flags]
```

Installs a node that waits in maintenance mode: a machine that booted the installer, or a
node's image booted for the first time. The node serves a self-signed certificate in maintenance
mode, so chalkctl verifies it by the SHA-256 fingerprint the node prints on its console
(--fingerprint), or accepts any certificate with --insecure and prints the fingerprint it saw,
which can be checked against the console afterwards.

chalkctl sends the node its config, checked against the schemas of the image it installs, a node
certificate with the node's name in the inventory, the OS CA, the secret of its volumes' second
keyslot and the payloads of its layers, such as a Kubernetes node's share. A node that runs its
image already installs in place. A node that runs the installer also gets the image --image names,
or the node's image in the inventory, and writes it to the disk its SystemDisk names: a disk on
which blkid finds no signature, one holding an unfinished install of the node, or any disk with
--wipe-disk. The node then reboots into the image. See
[An install turns a machine in maintenance mode into a node](../../how-it-works/node/overview.md#an-install-turns-a-machine-in-maintenance-mode-into-a-node).

A name revoked with chalkctl node revoke is refused, as is any node while a layer cannot say which
names are revoked, as when a bootstrapped Kubernetes cluster's control planes do not answer;
--force installs without asking. The command needs the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--fingerprint`{ #flag-fingerprint } | string | — | SHA-256 fingerprint the node prints on its console in maintenance mode. |
| `--force`{ #flag-force } | boolean | `false` | Install the node without asking whether its name is revoked. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--image`{ #flag-image } | string | the node's image in the inventory: from its directory, or from a registry, which the installer pulls | Image to install when the node runs the installer: a raw image with repart-output.json and repart.d next to it, or an image directory, which chalkctl sends. |
| `--insecure`{ #flag-insecure } | boolean | `false` | Accept any certificate and print the node's fingerprint. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--password-file`{ #flag-password-file } | string | — | File holding the password of a node whose fallback is a password. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--wipe-disk`{ #flag-wipe-disk } | boolean | `false` | Let the installer replace whatever the target disk holds, including an installed node; without it, the installer continues an earlier install of the node or takes a disk on which blkid finds no signature, which counts as empty even when it holds data. |

## Examples

```sh
# Install cp1, comparing the certificate with the fingerprint on its console.
chalkctl install cp1 --secrets secrets.age \
  --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08

# Install w1 at an address of the network it booted into, with an image chalkbuild signed.
chalkctl install w1 --secrets secrets.age --address 192.168.1.50 --insecure \
  --image ./worker-image
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
