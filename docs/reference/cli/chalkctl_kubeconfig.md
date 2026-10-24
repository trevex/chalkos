---
title: "chalkctl kubeconfig"
description: "Write an admin kubeconfig"
---

# chalkctl kubeconfig

```sh
chalkctl kubeconfig [flags]
```

Writes a kubeconfig with a client certificate for the Kubernetes API server, issued from the
secrets file for --name in the group chalkos:cluster-admins, which is bound to cluster-admin,
and valid for --ttl. The file holds the certificate's private key and is written with mode
0600. --server points clients at another URL than the cluster endpoint (a forwarded port, for
example), while they still verify the API server's certificate for the endpoint. The
kubeconfig's cluster, user and context are named after the inventory's metadata.name, or after
--context. The command needs the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--context`{ #flag-context } | string | the inventory's metadata.name | Name of the kubeconfig's cluster, user and context. |
| `--force`{ #flag-force } | boolean | `false` | Replace an existing file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--name`{ #flag-name } | string | `admin` | User name in the admin certificate. |
| `--out`{ #flag-out } | string | `kubeconfig` | File to write, - for standard output. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--server`{ #flag-server } | string | — | URL clients reach the API server at, when it differs from the cluster endpoint; the certificate is still verified for the endpoint. |
| `--ttl`{ #flag-ttl } | duration | `8760h0m0s` | Validity of the admin certificate. |

## Examples

```sh
chalkctl kubeconfig --secrets secrets.age --out lab.kubeconfig

# With the context home, to merge into a kubeconfig that holds other clusters.
chalkctl kubeconfig --secrets secrets.age --context home --out home.kubeconfig

# Through a port forwarded to the API server.
chalkctl kubeconfig --secrets secrets.age --server https://127.0.0.1:6443 --out -
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
