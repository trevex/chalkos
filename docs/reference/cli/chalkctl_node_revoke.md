---
title: "chalkctl node revoke"
description: "Cut a node off the cluster by name"
---

# chalkctl node revoke

Cut a node off the cluster by name

## Synopsis

Cuts a node off the cluster by name, at once and without a rotation. A control plane lists the
node in the ConfigMap kube-system/chalkos-revoked-nodes with the time of its revocation and
deletes the node's Node object; a node revoked already keeps its first time. From then on, control
planes refuse to renew the node's node certificate, chalkd approves none of its kubelet serving
certificates, and the API server denies every write by its kubelet (system:node:&lt;node>), which keeps
it from registering its Node again and from renewing its kubelet client certificate.

The node's certificates stay valid until they expire. Its kubelet can still read from the API
server, which grants a node with no Node and no pods almost nothing. A control plane's disk holds
the node CA's and the Kubernetes CA's keys: if a control plane's disk was stolen, rotate os-ca and
kubernetes-ca with chalkctl rotate as well. A cluster without Kubernetes keeps no revoked nodes:
there, revoking a node means not renewing its node certificate, and chalkctl rotate os-ca cuts it
off at once.

A control plane revokes the node: --via, or the first one that answers. The command needs an
admin client file or the secrets file.

```
chalkctl node revoke <node> [flags]
```

## Examples

```
  # Cut off w1, which was removed from the cluster.
  chalkctl node revoke w1
```

## Options

```
      --address string     address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
  -h, --help               help for revoke
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --secrets string     secrets file, encrypted with age or not, or - to read it from standard input
      --via string         control plane that revokes the node (default the first one that answers)
```

## SEE ALSO

* [chalkctl node](chalkctl_node.md)	 - Manage a node's certificate

