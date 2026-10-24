---
title: "chalkctl config new"
description: "Write a client file, which operates the cluster without the secrets file"
---

# chalkctl config new

```sh
chalkctl config new [flags]
```

Writes a client file: a new key with a certificate from the OS CA for --name and --role, and
the OS CA that nodes' certificates chain to. The file goes to ~/.config/chalkos/config unless --out
names another; commands read it from there, or from the path --config or $CHALKOSCONFIG names. A
client file names no cluster: its OS CA ties it to one, and a node of another cluster fails the TLS
handshake, which chalkctl reports as a node that does not accept the client file.

A client file works for status, logs, disks, reboot, storage reset, bootstrap, the etcd commands
and upgrade; every other command needs the secrets file. A reader may read nodes' information,
disks, status and logs and etcd's members; an operator may also reboot, upgrade, drain and
uncordon nodes; an admin may call every method of chalkd but the node's own renewal. See
[chalkd checks the role of every request](../../how-it-works/node/security.md#chalkd-checks-the-role-of-every-request).

The command needs the secrets file and no inventory.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--force`{ #flag-force } | boolean | `false` | Replace an existing file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--name`{ #flag-name } | string | — | The user's name, which the certificate carries. |
| `--out`{ #flag-out } | string | ~/.config/chalkos/config | File to write. |
| `--role`{ #flag-role } | string | — | The role the certificate grants: admin, operator or reader. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--ttl`{ #flag-ttl } | duration | `8760h0m0s` | Validity of the certificate. |

## Examples

```sh
# A reader's client file for a dashboard.
chalkctl config new --name grafana --role reader --ttl 720h --out grafana.json --secrets secrets.age

# The operator's own client file, at ~/.config/chalkos/config.
chalkctl config new --name alice --role operator --secrets secrets.age
```

## See also

- [chalkctl config](chalkctl_config.md): Manage client files
