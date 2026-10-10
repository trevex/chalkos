---
title: "chalkctl status"
description: "Show an installed node's status"
---

# chalkctl status

Show an installed node's status

## Synopsis

Shows an installed node's status: the identity it runs and whether it is the one the cluster
definition gives, its platform, its volumes and their state, Kubernetes, the certificates it
serves and when they expire, the CAs it trusts, its clock and the units that failed.

```
chalkctl status <node> [flags]
```

## Examples

```
  chalkctl status cp1

  # With a client file, as a lab writes.
  chalkctl status cp1 --config ~/.local/state/chalklab/lab/chalkctl.json
```

## Options

```
      --cluster string     cluster to use when the flake defines several
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)
      --endpoint string    address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --flake string       directory of the flake that defines the cluster (default ".")
  -h, --help               help for status
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string    read the cluster's manifest from this file instead of evaluating the flake
      --secrets string     secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

