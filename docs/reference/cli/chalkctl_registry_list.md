---
title: "chalkctl registry list"
description: "List the images of every endpoint of the cluster registry and mark those that differ"
---

# chalkctl registry list

```sh
chalkctl registry list [flags]
```

Lists the repositories, tags and digests of every endpoint of the cluster's ClusterRegistry, with
the endpoints that serve each. A tag's digest is the one most endpoints serve; the command marks
an endpoint that lacks the tag, serves it with another digest, or lacks images of a tagged index,
which would fail a pull of that platform. Nothing copies images between the endpoints, so the
command exits 1 when any is marked or an endpoint cannot be listed; a push of the image to every
endpoint mends it. Images pushed by digest alone, without a tag, are not listed.

The registry serves reads to anyone, but chalkctl verifies the endpoints by the OS CA of a client
file or the secrets file, so the command needs a client file of any role or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--connect-to`{ #flag-connect-to } | list of string | — | ENDPOINT=HOST:PORT: reach an endpoint at another address, such as a forwarded port, while still verifying its certificate for the endpoint; may be repeated, once per endpoint. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |

## Examples

```sh
chalkctl registry list
```

## See also

- [chalkctl registry](chalkctl_registry.md): Manage the images of the cluster registry
