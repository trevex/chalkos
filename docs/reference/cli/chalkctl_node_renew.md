---
title: "chalkctl node renew"
description: "Issue a node a new node certificate, also once its own expired"
---

# chalkctl node renew

```sh
chalkctl node renew <node> [flags]
```

Issues the node a new node certificate and key from the node CA and delivers them, also once the
node's own certificate expired. Such a node is verified by the OS CA as of its certificate's
start, so chalkctl trusts the node's old key: someone holding a leaked, expired key of the node
and sitting in its network path could receive the new certificate in its place. That is inherent
to recovering a node; renewing node certificates before they expire avoids it.

A node revoked with chalkctl node revoke is refused, as is any node while no control plane says
which nodes are revoked: the control planes are asked first. A control plane whose own
certificate expired cannot answer; --force renews without asking. The command needs the secrets
file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--force`{ #flag-force } | boolean | `false` | Renew without asking the control planes whether the node is revoked. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
chalkctl node renew w1
```

## See also

- [chalkctl node](chalkctl_node.md): Manage a node's access and run its diagnostics
