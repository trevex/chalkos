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
      --cluster string     cluster to use when the flake defines several
      --flake string       directory of the flake that defines the cluster (default ".")
  -h, --help               help for recovery-key
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string    read the cluster's manifest from this file instead of evaluating the flake
      --secrets string     secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

