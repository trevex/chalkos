---
title: "chalkctl recovery-key"
description: "Print a node's recovery key"
---

# chalkctl recovery-key

Print a node's recovery key

## Synopsis

Prints the recovery key of a node, which unlocks its encrypted volumes when the TPM does not:
for example after a change of the Secure Boot keys or state, which the TPM measures in PCR 7. It
is derived from the secrets file's recovery secret, the cluster's name and the node's name, so no
file stores it. The command needs the secrets file.

```
chalkctl recovery-key <node> [flags]
```

## Examples

```
  chalkctl recovery-key w1
```

## Options

```
  -h, --help               help for recovery-key
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --manifest string    read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --secrets string     secrets file, - for standard input (default the inventory's, else secrets.age, else secrets.json, beside the inventory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Sign, install and operate chalkos clusters

