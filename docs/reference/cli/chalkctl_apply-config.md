---
title: "chalkctl apply-config"
description: "Deliver a node's config from the cluster definition"
---

# chalkctl apply-config

Deliver a node's config from the cluster definition

## Synopsis

Delivers a node's config to the installed node: the documents of the files the inventory gives
it. chalkctl first asks the node for the contract of the image it runs: its platform, schemas and
image.json. It refuses an image of another platform than the node's Image, and checks the
documents as the node does. The node replaces its config as a whole, and applies
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
      --address string         address of the node's chalkd, host or host:port (default the node's address in the inventory)
  -h, --help                   help for apply-config
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string       inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --kubernetes-share       also deliver a new Kubernetes share, such as a new kubelet certificate for a worker
      --manifest string        read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --password-file string   file holding the password of a node whose fallback is a password
      --secrets string         secrets file, - for standard input (default the inventory's, else secrets.age, else secrets.json, beside the inventory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Sign, install and operate chalkos clusters

