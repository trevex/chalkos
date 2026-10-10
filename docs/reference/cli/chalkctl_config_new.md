---
title: "chalkctl config new"
description: "Write a client file, which operates the cluster without the secrets file"
---

# chalkctl config new

Write a client file, which operates the cluster without the secrets file

## Synopsis

Writes a client file: a new key with a certificate from the OS CA for --name and --role, the OS
CA that nodes' certificates chain to, and the nodes' addresses. A client file works for status,
logs, disks, reboot, storage reset, bootstrap, the etcd commands and upgrade; every other command
needs the secrets file. The role decides what chalkd allows: a reader may read nodes'
information, disks, status and logs and etcd's members; an operator may also reboot, upgrade,
drain and uncordon nodes; an admin may call every method of chalkd except RenewNodeCertificate,
which needs the node role (nodes call it to renew their own certificates). The file is written to
~/.config/chalkos/config unless --out names another; commands read it from there, or from the
path --config or $CHALKOSCONFIG names. The command needs the secrets file.

```
chalkctl config new [flags]
```

## Examples

```
  # A reader's client file for a dashboard.
  chalkctl config new --name grafana --role reader --ttl 720h --out grafana.json

  # The operator's own client file, at ~/.config/chalkos/config.
  chalkctl config new --name alice --role operator
```

## Options

```
      --cluster string     cluster to use when the flake defines several
      --flake string       directory of the flake that defines the cluster (default ".")
      --force              replace an existing file
  -h, --help               help for new
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string    read the cluster's manifest from this file instead of evaluating the flake
      --name string        the user's name, which the certificate carries
      --out string         file to write (default ~/.config/chalkos/config)
      --role string        the role the certificate grants: admin, operator or reader
      --secrets string     secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
      --ttl duration       validity of the certificate (default 8760h0m0s)
```

## SEE ALSO

* [chalkctl config](chalkctl_config.md)	 - Manage client files

