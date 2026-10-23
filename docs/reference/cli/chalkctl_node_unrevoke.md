---
title: "chalkctl node unrevoke"
description: "Let a revoked node's name join the cluster again"
---

# chalkctl node unrevoke

Let a revoked node's name join the cluster again

## Synopsis

Removes a node from the revoked nodes, so a node of that name may join the cluster again. The old
node's certificates then work again too, unless the CAs they chain to were rotated after the
revocation: chalkd trusts every node CA the OS CA signed, and the kubelet's certificates chain to
the Kubernetes CA. The control plane therefore refuses while the OS CA or the Kubernetes CA
predates the revocation, and names the rotations missing. --force unrevokes the node anyway, and
its old certificates work again until they expire. The check looks at the OS CA and the
Kubernetes CA alone. A former control plane's etcd and front-proxy certificates are beyond
revocation either way: they stay valid until chalkctl rotate kubernetes-ca, which replaces those
CAs along with the Kubernetes CA, has finished.

To reuse a revoked node's name safely, rotate both CAs first, then unrevoke it:
chalkctl rotate os-ca and chalkctl rotate kubernetes-ca, each to its finish. A node installed
afresh under the name then joins as usual.

The old node does not come back by itself after a forced unrevoke: its kubelet registers its Node
once as it starts, and not again when the Node is deleted, as the revocation did. Reboot the old
node with chalkctl reboot, or restart its kubelet.

A control plane unrevokes the node: --via, or the first one that answers. The command needs an
admin client file or the secrets file.

```
chalkctl node unrevoke <node> [flags]
```

## Examples

```
  # Reuse the name w1 once the rotations of os-ca and kubernetes-ca have finished.
  chalkctl node unrevoke w1

  # Let the old w1 back in, certificates and all.
  chalkctl node unrevoke w1 --force
  chalkctl reboot w1
```

## Options

```
      --address string     address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
      --force              unrevoke the node although the OS CA or the Kubernetes CA predates its revocation, so its old certificates work again
  -h, --help               help for unrevoke
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --secrets string     secrets file, encrypted with age or not, or - to read it from standard input
      --via string         control plane that unrevokes the node (default the first one that answers)
```

## SEE ALSO

* [chalkctl node](chalkctl_node.md)	 - Manage a node's certificate

