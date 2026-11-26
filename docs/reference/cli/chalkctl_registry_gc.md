---
title: "chalkctl registry gc"
description: "Collect garbage on every endpoint of the cluster registry"
---

# chalkctl registry gc

```sh
chalkctl registry gc [flags]
```

Collects garbage on every endpoint of the cluster's ClusterRegistry, one after the other: each
removes the layers and configs that no manifest references, keeps every manifest, tagged or not,
and reports the space it freed. Nothing collects garbage on its own, so run the command after
registry delete of a digest; deleting a tag frees nothing, since its manifest stays.

While an endpoint collects, it serves pulls and refuses pushes and deletes, and a push whose
layers went up before the collection started may fail on them, so run the command when no push
is in progress. An endpoint that collects already says so, and the command fails for it. A
collection runs to its end on the endpoint once begun: Ctrl-C stops chalkctl, not the
collection.

The command needs an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Admin client file to authenticate with instead of the secrets file. |
| `--connect-to`{ #flag-connect-to } | list of string | — | ENDPOINT=HOST:PORT: reach an endpoint at another address, such as a forwarded port, while still verifying its certificate for the endpoint; may be repeated, once per endpoint. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |

## Examples

```sh
chalkctl registry delete docker.io/library/busybox@sha256:7b3ccabffc97de872a30dfd234fd972a66d247c8cfc69b0550f276481852627c --with-tags
chalkctl registry gc
```

## See also

- [chalkctl registry](chalkctl_registry.md): Manage the images of the cluster registry
