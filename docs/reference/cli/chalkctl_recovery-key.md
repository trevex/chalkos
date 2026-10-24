---
title: "chalkctl recovery-key"
description: "Print a node's recovery key"
---

# chalkctl recovery-key

```sh
chalkctl recovery-key <node> [flags]
```

Prints the recovery key of a node, which unlocks its encrypted volumes when the TPM does not:
for example after a change of the Secure Boot keys or state, which the TPM measures in PCR 7. It
is derived from the secrets file's recovery secret and the node's name, so no file stores it. The
command needs the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
chalkctl recovery-key w1
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
