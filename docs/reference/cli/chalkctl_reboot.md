---
title: "chalkctl reboot"
description: "Reboot a node"
---

# chalkctl reboot

```sh
chalkctl reboot <node> [flags]
```

Reboots a node, installed or in maintenance mode. An installed node does not drain its
Kubernetes pods first. The command needs an operator client file or the secrets file; a node in
maintenance mode is reached with --fingerprint or --insecure as well.

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
chalkctl reboot w1
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
