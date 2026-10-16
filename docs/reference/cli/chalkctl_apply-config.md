---
title: "chalkctl apply-config"
description: "Deliver a node's config from the cluster definition"
---

# chalkctl apply-config

Deliver a node's config from the cluster definition

## Synopsis

Delivers a node's config to the installed node: the documents chalkctl generates from the
cluster definition, which set its hostname, networks, storage, time servers, Kubernetes settings
(its kind, labels, taints, addresses and subnets, and the cluster's Kubernetes settings) and the
documents of its extensions' kinds. chalkctl first asks the node for the contract of the image it
runs: its role, platform, schemas and image.json. It refuses an image of another role or platform
than the cluster definition gives the node, and checks that the documents give back what the
cluster definition declares for the node. The node replaces its config as a whole, and applies
additive storage changes (a new volume, for example) while it runs. When the config changes a
volume destructively, the node refuses the whole config and applies none of it; chalkctl storage
reset recreates such a volume. Otherwise the node restarts the units that read what changed, and
chalkctl prints the changes and the units. --kubernetes-share also delivers a new Kubernetes
share: for example a worker's new kubelet certificate. The command needs the secrets file.

```
chalkctl apply-config <node> [flags]
```

## Examples

```
  # Apply w1's changed network and labels.
  chalkctl apply-config w1

  # Give w1 a new kubelet certificate.
  chalkctl apply-config w1 --kubernetes-share
```

## Options

```
      --cluster string         cluster to use when the flake defines several
      --endpoint string        address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --flake string           directory of the flake that defines the cluster (default ".")
  -h, --help                   help for apply-config
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --kubernetes-share       also deliver a new Kubernetes share, such as a new kubelet certificate for a worker
      --manifest string        read the cluster's manifest from this file instead of evaluating the flake
      --password-file string   file holding the password of a node whose fallback is a password
      --secrets string         secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

