---
title: "chalkctl logs"
description: "Show a node's journal"
---

# chalkctl logs

```sh
chalkctl logs <node> [flags]
```

Prints a node's journal of the current boot, of every unit or of --unit alone; with --follow it
keeps printing new entries until interrupted, starting with the last 100 entries. --lines starts
with that many of the last entries, or with the whole boot at 0. A node in maintenance mode is
reached with --fingerprint or --insecure as well.

The command needs a reader client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: ADDR for the one node the command reaches, or NODE=ADDR for any node, where ADDR is host or host:port; may be repeated. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--fingerprint`{ #flag-fingerprint } | string | — | SHA-256 fingerprint the node prints on its console in maintenance mode. |
| `--follow`{ #flag-follow }, `-f` | boolean | `false` | Keep printing new entries. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--insecure`{ #flag-insecure } | boolean | `false` | Accept any certificate and print the node's fingerprint. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--lines`{ #flag-lines }, `-n` | uint32 | 100 with --follow, else the whole boot | How many of the boot's last entries to start with, 0 for the whole boot. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |
| `--unit`{ #flag-unit } | string | — | Unit whose entries alone are printed. |

## Examples

```sh
# Follow chalkd's log on cp1.
chalkctl logs cp1 --unit chalkd.service -f

# The last 20 entries of the boot.
chalkctl logs cp1 -n 20
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
