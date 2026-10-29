---
title: "chalkctl apply-config"
description: "Deliver a node's config from the inventory"
---

# chalkctl apply-config

```sh
chalkctl apply-config <node> [flags]
```

Delivers a node's config to the installed node: the documents of the files the inventory gives
it. chalkctl checks them against the contract of the image the node runs, with the image's change
rules that read no facts, and the node checks them again. The node replaces its config as a whole,
applies additive storage changes such as a new volume while it runs, and restarts the units that
read what changed; chalkctl prints the changes and the units. When the image's rules refuse a
change, the node refuses the whole config and names the remedy. See
[Change rules decide which changes a node accepts](../../how-it-works/node/config.md#change-rules-decide-which-changes-a-node-accepts).

Each image on a node keeps its own config. --staged delivers the config for the image staged to
boot next, as an upgrade with --no-reboot leaves it, and nothing restarts. Without --staged,
chalkctl warns of a staged image, whose config stays as it is. A config for the running image that
adds storage the staged image's config lacks is refused: deliver the config with --staged first.

--kubernetes-share also delivers a new Kubernetes share, such as a worker's new kubelet
certificate. It is refused to a node revoked with chalkctl node revoke, and while no control plane
says which nodes are revoked; --force delivers it without asking. The command needs the secrets
file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--diff`{ #flag-diff } | boolean | `false` | Print what the config changes in the node's config, document by document and field by field, and change nothing. |
| `--force`{ #flag-force } | boolean | `false` | With --kubernetes-share, deliver without asking whether the node is revoked. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--kubernetes-share`{ #flag-kubernetes-share } | boolean | `false` | Also deliver a new Kubernetes share, such as a new kubelet certificate for a worker. |
| `--password-file`{ #flag-password-file } | string | — | File holding the password of a node whose fallback is a password. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--staged`{ #flag-staged } | boolean | `false` | Deliver the config for the image staged to boot next instead, checked against the node's image in the inventory. |

## Examples

```sh
# Apply w1's changed network and labels.
chalkctl apply-config w1 --secrets secrets.age

# Give w1 a new kubelet certificate.
chalkctl apply-config w1 --secrets secrets.age --kubernetes-share

# Show what w1's config in the inventory changes on the node, changing nothing.
chalkctl apply-config w1 --secrets secrets.age --diff

# Deliver w1's config for the image an upgrade with --no-reboot staged.
chalkctl apply-config w1 --secrets secrets.age --staged
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
