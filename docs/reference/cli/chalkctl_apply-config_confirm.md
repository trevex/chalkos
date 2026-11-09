---
title: "chalkctl apply-config confirm"
description: "Confirm a node's config on trial"
---

# chalkctl apply-config confirm

```sh
chalkctl apply-config confirm <node> [flags]
```

Confirms the open trial of a node's config, as apply-config --try does without --no-confirm, so
the node keeps the config. A trial stays open after apply-config --try --no-confirm, after
apply-config --until deliver, and when chalkctl was interrupted.

chalkctl first waits for the image's layers to find the node serving with the config, as
Kubernetes finds its Node Ready and a control plane's static pods on the images its config names;
when they do not by the deadline, chalkctl rolls the config back at once. It then confirms over a
new connection once the node is healthy, trying until the trial's deadline while it is not, or for
two minutes past it after a failed rollback, which the node still lets a confirmation end. A
rollback that failed after it changed the node can no longer be confirmed: apply-config rollback
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
chalkctl apply-config w1 --secrets secrets.age --try 30m --no-confirm
chalkctl apply-config confirm w1 --secrets secrets.age
```

## See also

- [chalkctl apply-config](chalkctl_apply-config.md): Deliver a node's config from the inventory
