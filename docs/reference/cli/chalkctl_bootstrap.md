---
title: "chalkctl bootstrap"
description: "Initialise the cluster on a control plane"
---

# chalkctl bootstrap

Initialise the cluster on a control plane

## Synopsis

Initialises the cluster on one control plane: the node starts etcd as its first member, starts
the Kubernetes control-plane components and applies chalkos's add-ons and the objects of its Addon
documents. chalkctl waits until they are applied, up to --timeout. The node refuses when it is
bootstrapped already or holds etcd data, so a second cluster is never initialised; the other
control planes join the first. The command needs an admin client file or the secrets file.

```
chalkctl bootstrap <node> [flags]
```

## Examples

```
  chalkctl bootstrap cp1
```

## Options

```
      --address string     address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
  -h, --help               help for bootstrap
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --secrets string     secrets file, encrypted with age or not, or - to read it from standard input
      --timeout duration   how long to wait for the control plane to apply its add-ons and Addon objects (default 20m0s)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Install and operate chalkos clusters

