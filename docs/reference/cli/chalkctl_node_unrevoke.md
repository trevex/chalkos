---
title: "chalkctl node unrevoke"
description: "Let a revoked node's name join the cluster again"
---

# chalkctl node unrevoke

```sh
chalkctl node unrevoke <node> [flags]
```

Removes a node from the revoked nodes, so a node of that name may join the cluster again. The old
node's certificates would work again too, so the control plane refuses while the OS CA or the
Kubernetes CA predates the revocation, and names the rotations missing. To reuse the name, run
chalkctl rotate os-ca and chalkctl rotate kubernetes-ca, each to its finish, first; a node
installed afresh under the name then joins as usual. See
[Revoking a node cuts off its writes and renewals](../../how-it-works/kubernetes/certificates.md#revoking-a-node-cuts-off-its-writes-and-renewals).

--force unrevokes the node anyway, and its old certificates work again until they expire. The old
node does not come back by itself: its kubelet registers its Node once as it starts, so reboot it
with chalkctl reboot, or restart its kubelet.

A control plane unrevokes the node: --via, or the first one that answers. The command needs an
admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--force`{ #flag-force } | boolean | `false` | Unrevoke the node although the OS CA or the Kubernetes CA predates its revocation, so its old certificates work again. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--via`{ #flag-via } | string | the first one that answers | Control plane that unrevokes the node. |

## Examples

```sh
# Reuse the name w1 once the rotations of os-ca and kubernetes-ca have finished.
chalkctl node unrevoke w1

# Let the old w1 back in, certificates and all.
chalkctl node unrevoke w1 --force
chalkctl reboot w1
```

## See also

- [chalkctl node](chalkctl_node.md): Manage a node's access and show its diagnostics and routes
