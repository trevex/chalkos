---
title: "chalkctl apply-config"
description: "Deliver a node's config from the inventory"
---

# chalkctl apply-config

Deliver a node's config from the inventory

## Synopsis

Delivers a node's config to the installed node: the documents of the files the inventory gives
it. chalkctl first asks the node for the contract of the image it runs: its platform, schemas and
image.json. It refuses an image of another platform than the node's Image, and checks the
documents as the node does. The node replaces its config as a whole, and applies additive
storage changes (a new volume, for example) while it runs. When the config changes a volume
destructively, the node refuses the whole config, applies none of it, and names the remedy:
chalkctl storage reset recreates a volume in place, once the disk it lives on is plugged in; a
change to VAR, a removed volume or a move to another disk takes a reinstall; a changed disk
reference that finds no disk needs fixing or its disk plugged in, and one that cannot otherwise
be resolved, as it finds more than one disk or names something other than a whole disk, needs
fixing. Otherwise the node restarts the units that read what changed, and chalkctl prints the
changes and the units. --kubernetes-share also delivers a new Kubernetes share: for example a
worker's new kubelet certificate. The command needs the secrets file.

```
chalkctl apply-config <node> [flags]
```

## Examples

```
  # Apply w1's changed network and labels.
  chalkctl apply-config w1 --secrets secrets.age

  # Give w1 a new kubelet certificate.
  chalkctl apply-config w1 --secrets secrets.age --kubernetes-share
```

## Options

```
      --address string         address of the node's chalkd, host or host:port (default the node's address in the inventory)
  -h, --help                   help for apply-config
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string       inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --kubernetes-share       also deliver a new Kubernetes share, such as a new kubelet certificate for a worker
      --password-file string   file holding the password of a node whose fallback is a password
      --secrets string         secrets file, encrypted with age or not, or - to read it from standard input
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Sign, install and operate chalkos clusters

