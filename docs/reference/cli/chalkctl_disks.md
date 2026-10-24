---
title: "chalkctl disks"
description: "List a node's disks"
---

# chalkctl disks

```sh
chalkctl disks [<node>] [flags]
```

Lists a node's disks with their use, the partitions on them, and the properties a storage
definition selects disks by: size, type, model, serial number and WWN. An installed node is
reached with a reader client file or the secrets file; a node in maintenance mode with
--fingerprint or --insecure as well. A node that the inventory does not name yet is reached at
--address, in maintenance mode: an image or installer built with an OS CA accepts only a client
with the secrets file or a client file, one built without accepts any client.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--fingerprint`{ #flag-fingerprint } | string | — | SHA-256 fingerprint the node prints on its console in maintenance mode. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--insecure`{ #flag-insecure } | boolean | `false` | Accept any certificate and print the node's fingerprint. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
# The disks of an installed node.
chalkctl disks w1

# The disks of a machine that booted the installer and is not in the inventory yet.
chalkctl disks --address 192.168.1.50 --insecure --secrets secrets.age
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
