---
title: "chalkctl node revoke"
description: "Cut a node off the cluster by name"
---

# chalkctl node revoke

```sh
chalkctl node revoke <node> [flags]
```

Cuts a node off the cluster by name, at once and without a rotation. A control plane lists the
node, with the time of its first revocation, in the ConfigMap kube-system/chalkos-revoked-nodes and
deletes its Node object. From then on
the control planes renew none of its certificates, the API server denies every write by its
kubelet, and chalkctl rotate skips it. Its certificates stay valid until they expire, so its kubelet
can still read what every kubelet may. See
[Revoking a node cuts off its writes and renewals](../../how-it-works/kubernetes/certificates.md#revoking-a-node-cuts-off-its-writes-and-renewals).

Revoking a control plane does not cut it off, because its disk holds the node CA's and the
Kubernetes CA's keys. The command refuses a node that is still an etcd member, and the control
plane --via names: take it out with chalkctl etcd leave, or chalkctl etcd remove-member once it is
gone, then rotate os-ca and kubernetes-ca. A cluster without Kubernetes keeps no revoked nodes, and
there chalkctl rotate os-ca cuts a node off.

The command first asks every other control plane whether it reads the revoked nodes, and refuses
while one does not answer or does not say it read them. --force revokes the node despite either
refusal. A control plane revokes the node: --via, or the first other one that answers. The command
needs an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--force`{ #flag-force } | boolean | `false` | Revoke the node although it is a control plane, an etcd member or the one --via names, or although a control plane does not say it reads the revoked nodes. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--via`{ #flag-via } | string | the first other one that answers | Control plane that revokes the node. |

## Examples

```sh
# Cut off w1, which was removed from the cluster.
chalkctl node revoke w1
```

## See also

- [chalkctl node](chalkctl_node.md): Manage a node's certificate
