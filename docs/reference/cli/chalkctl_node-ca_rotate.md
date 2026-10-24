---
title: "chalkctl node-ca rotate"
description: "Issue a new node CA and deliver it to the control planes"
---

# chalkctl node-ca rotate

```sh
chalkctl node-ca rotate [flags]
```

Issues a new node CA from the OS CA, writes it to the secrets file and delivers it to every
control plane; the control planes issue node certificates from it from then on. Node certificates
of the old node CA stay valid until they expire, because they chain to the same OS CA. The
secrets file is updated in place, keeping its previous version as &lt;file>.prev, unless --out names
a new file. The command needs the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--out`{ #flag-out } | string | — | New file to write the changed secrets file to, instead of updating the secrets file in place. |
| `--public-out`{ #flag-public-out } | string | — | New file to write the public half to, instead of updating secrets.pub.json beside the secrets file in place. |
| `--recipient`{ #flag-recipient } | list of string | — | Age recipient to encrypt the changed secrets file to instead of those it records; may be repeated. A plaintext secrets file is encrypted only to a new .age file given with --out. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
chalkctl node-ca rotate

# Write the changed secrets to a new file and keep the old one.
chalkctl node-ca rotate --secrets secrets.age --out secrets.new.age --public-out secrets.pub.new.json
```

## See also

- [chalkctl node-ca](chalkctl_node-ca.md): Manage the node CA
