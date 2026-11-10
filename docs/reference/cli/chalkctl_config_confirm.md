---
title: "chalkctl config confirm"
description: "Confirm a node's config on trial"
---

# chalkctl config confirm

```sh
chalkctl config confirm <node> [flags]
```

Confirms the open trial of a node's config, as config apply --try does without --no-confirm, so
the node keeps the config. A trial stays open after config apply --try --no-confirm, after
config apply --until deliver, and when chalkctl was interrupted.

chalkctl first waits for the image's layers to find the node serving with the config, as
Kubernetes finds its Node Ready and a control plane's static pods on the images its config names;
when they do not by the deadline, chalkctl rolls the config back at once. It then confirms over a
new connection once the node is healthy, trying until the trial's deadline while it is not, or for
two minutes past it after a failed rollback, which the node still lets a confirmation end. A
rollback that failed after it changed the node can no longer be confirmed: config rollback
finishes it.

The node keeps the config on trial, so the command needs no inventory. It needs the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: host or host:port for the node the command names, or NODE=ADDR for any node; may be repeated. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
# Keep a trial open while you test by hand, then keep the config.
chalkctl config apply w1 --secrets secrets.age --try 30m --no-confirm
chalkctl config confirm w1 --secrets secrets.age
```

## See also

- [chalkctl config](chalkctl_config.md): Deliver a node's config and act on its trial or pending config
