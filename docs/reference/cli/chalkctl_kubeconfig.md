---
title: "chalkctl kubeconfig"
description: "Write an admin kubeconfig"
---

# chalkctl kubeconfig

Write an admin kubeconfig

## Synopsis

Writes a kubeconfig with a client certificate for the Kubernetes API server, issued from the
secrets file for --name in the group chalkos:cluster-admins, which is bound to cluster-admin,
and valid for --ttl. The file holds the certificate's private key and is written with mode
0600. --server points clients at another URL than the cluster endpoint (a forwarded port, for
example), while they still verify the API server's certificate for the endpoint. The command
needs the secrets file.

```
chalkctl kubeconfig [flags]
```

## Examples

```
  chalkctl kubeconfig --out lab.kubeconfig

  # Through a port forwarded to the API server.
  chalkctl kubeconfig --server https://127.0.0.1:6443 --out -
```

## Options

```
      --cluster string     cluster to use when the flake defines several
      --flake string       directory of the flake that defines the cluster (default ".")
      --force              replace an existing file
  -h, --help               help for kubeconfig
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string    read the cluster's manifest from this file instead of evaluating the flake
      --name string        user name in the admin certificate (default "admin")
      --out string         file to write, - for standard output (default "kubeconfig")
      --secrets string     secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
      --server string      URL clients reach the API server at, when it differs from the cluster endpoint; the certificate is still verified for the endpoint
      --ttl duration       validity of the admin certificate (default 8760h0m0s)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

