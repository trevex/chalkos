---
title: "chalkctl config rollback"
description: "Deliver a node's previous config again"
---

# chalkctl config rollback

```sh
chalkctl config rollback <node> [flags]
```

Delivers a node's previous config again: the config the running image ran before the last
delivery, which the node keeps. It goes through the same checks, storage changes and restarts as
any delivery, so the image's rules may refuse it, as when the config it replaces added a volume.
The config it replaces becomes the previous one, so a second rollback undoes the first. It restores
the config documents alone: the OS CAs, the node certificate and layer payloads such as a
Kubernetes share stay as they are. An upgrade's image starts without a previous config. --diff
prints what the rollback changes and changes nothing.

During a trial, the rollback goes to the config from before the trial at once and ends the trial.
It also finishes a rollback at a trial's deadline that failed after it changed the node. After a
trial's rollback, the config on trial is the node's previous config, which the command delivers
again without a trial.

The node keeps the config it rolls back to, so the command needs no inventory. It needs the
secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: host or host:port for the node the command names, or NODE=ADDR for any node; may be repeated. |
| `--diff`{ #flag-diff } | boolean | `false` | Print what the rollback changes in the node's config, document by document and field by field, and change nothing. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
# Show what undoing w1's last config delivery changes, then undo it.
chalkctl config rollback w1 --secrets secrets.age --diff
chalkctl config rollback w1 --secrets secrets.age
```

## See also

- [chalkctl config](chalkctl_config.md): Deliver nodes' config and manage client files
