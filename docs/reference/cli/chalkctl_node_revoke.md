---
title: "chalkctl node revoke"
description: "Cut a node off the cluster by name"
---

# chalkctl node revoke

```sh
chalkctl node revoke <node> [flags]
```

Cuts a node off the cluster by name, at once and without a rotation. A control plane lists the
node in the ConfigMap kube-system/chalkos-revoked-nodes with the time of its revocation and
deletes the node's Node object; a node revoked already keeps its first time. From then on, control
planes refuse to renew the node's node certificate, chalkd approves none of its kubelet serving
certificates, the API server denies every write by its kubelet (system:node:&lt;node>), which keeps
it from registering its Node again and from renewing its kubelet client certificate, and chalkctl
rotate skips it.

The node's certificates stay valid until they expire, and the API server's admission sees writes
alone. So the node's kubelet can still read what every kubelet may: Services, Endpoints and
EndpointSlices, and a few cluster-scoped kinds such as Nodes and RuntimeClasses. The Secrets and
ConfigMaps of the pods bound to it stay readable to it until the pod garbage collector deletes
those pods, shortly after their Node is gone.

Revoking a control plane does not cut it off: its disk holds the node CA's and the Kubernetes
CA's keys. The command refuses a node that is still an etcd member, and the control plane --via
names; take such a node out with chalkctl etcd leave, or chalkctl etcd remove-member once it is
gone, then rotate os-ca and kubernetes-ca with chalkctl rotate. --force revokes it all the same.
The same rotations cut off whoever stole a control plane's disk. A cluster without Kubernetes
keeps no revoked nodes: there, revoking a node means not renewing its node certificate, and
chalkctl rotate os-ca cuts it off at once.

Before it revokes the node, the command asks every other control plane of the inventory, or the
one --via names without an inventory, whether it reads the revoked nodes. A control plane whose
chalkd predates node revocation renews the node's node certificate until it is upgraded, so the
command refuses while one does not answer, cannot read the revoked nodes, or does not say it read
them; --force revokes the node anyway. A control plane revokes the node: --via, or the first other
one that answers. The command needs an admin client file or the secrets file.

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
