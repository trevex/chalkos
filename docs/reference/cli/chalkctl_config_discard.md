---
title: "chalkctl config discard"
description: "Drop the config a node holds pending for its next upgrade"
---

# chalkctl config discard

```sh
chalkctl config discard <node> [flags]
```

Drops the config a node holds pending for its next upgrade, which config apply --staged leaves on
a node without a staged image, and names the config it dropped. The node holds the config, so the
command needs no inventory. It needs the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: host or host:port for the node the command names, or NODE=ADDR for any node; may be repeated. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
# Drop the config w1 holds pending.
chalkctl config discard w1 --secrets secrets.age
```

## See also

- [chalkctl config](chalkctl_config.md): Deliver a node's config and act on its trial or pending config
