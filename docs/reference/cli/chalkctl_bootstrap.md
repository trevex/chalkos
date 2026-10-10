---
title: "chalkctl bootstrap"
description: "Initialise the cluster on a control-plane node"
---

# chalkctl bootstrap

Initialise the cluster on a control-plane node

## Synopsis

Initialises the cluster on one control-plane node: the node starts etcd as its first member,
starts the control plane and applies the cluster's manifests. chalkctl waits until they are
applied, up to --timeout. The node refuses when it is bootstrapped already or holds etcd data,
so a second cluster is never initialised; the other control planes join the first.

```
chalkctl bootstrap <node> [flags]
```

## Examples

```
  chalkctl bootstrap cp1
```

## Options

```
      --cluster string     cluster to use when the flake defines several
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)
      --endpoint string    address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --flake string       directory of the flake that defines the cluster (default ".")
  -h, --help               help for bootstrap
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string    read the cluster's manifest from this file instead of evaluating the flake
      --secrets string     secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
      --timeout duration   how long to wait for the control plane to apply its manifests (default 20m0s)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

