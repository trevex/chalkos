---
title: "chalkctl apply-identity"
description: "Deliver a node's identity from the cluster definition"
---

# chalkctl apply-identity

Deliver a node's identity from the cluster definition

## Synopsis

Delivers a node's identity from the cluster definition to the installed node: its hostname,
networks, labels, taints, storage, time servers, Kubernetes settings (node name, addresses and
subnets) and extensions. The node applies additive storage changes (a new volume, for example)
while it runs. When the identity changes a volume destructively, the node refuses the whole
identity and applies none of it; chalkctl storage reset recreates such a volume. Otherwise the
node restarts the units that read what changed, and chalkctl prints the changes and the units.
--kubernetes-share also delivers a new Kubernetes share: for example a worker's new kubelet
certificate. The command needs the secrets file.

```
chalkctl apply-identity <node> [flags]
```

## Examples

```
  # Apply w1's changed network and labels.
  chalkctl apply-identity w1

  # Give w1 a new kubelet certificate.
  chalkctl apply-identity w1 --kubernetes-share
```

## Options

```
      --cluster string         cluster to use when the flake defines several
      --endpoint string        address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --flake string           directory of the flake that defines the cluster (default ".")
  -h, --help                   help for apply-identity
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --kubernetes-share       also deliver a new Kubernetes share, such as a new kubelet certificate for a worker
      --manifest string        read the cluster's manifest from this file instead of evaluating the flake
      --password-file string   file holding the password of a node whose fallback is a password
      --secrets string         secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

