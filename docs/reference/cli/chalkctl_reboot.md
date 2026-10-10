---
title: "chalkctl reboot"
description: "Reboot a node"
---

# chalkctl reboot

Reboot a node

## Synopsis

Reboots a node, installed or in maintenance mode. An installed node does not drain its
Kubernetes pods first.

```
chalkctl reboot <node> [flags]
```

## Examples

```
  chalkctl reboot w1
```

## Options

```
      --cluster string       cluster to use when the flake defines several
      --config string        client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)
      --endpoint string      address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --fingerprint string   SHA-256 fingerprint the node prints on its console in maintenance mode
      --flake string         directory of the flake that defines the cluster (default ".")
  -h, --help                 help for reboot
      --identity strings     age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --insecure             accept any certificate and print the node's fingerprint
      --manifest string      read the cluster's manifest from this file instead of evaluating the flake
      --secrets string       secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

