---
title: "chalkctl bootstrap"
description: "Initialise the cluster on a control plane"
---

# chalkctl bootstrap

```sh
chalkctl bootstrap <node> [flags]
```

Initialises the cluster on one control plane: the node starts etcd as its first member, starts
the Kubernetes control-plane components and applies chalkos's add-ons and the objects of its Addon
documents. chalkctl waits until they are applied, up to --timeout. The node refuses when it is
bootstrapped already or holds etcd data, so a second cluster is never initialised; the other
control planes join the first. The command needs an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--timeout`{ #flag-timeout } | duration | `20m0s` | How long to wait for the control plane to apply its add-ons and Addon objects. |

## Examples

```sh
chalkctl bootstrap cp1
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
