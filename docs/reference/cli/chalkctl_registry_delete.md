---
title: "chalkctl registry delete"
description: "Delete a tag or digest on every endpoint of the cluster registry"
---

# chalkctl registry delete

```sh
chalkctl registry delete REFERENCE [flags]
```

Deletes REFERENCE, a tag or a digest, below every endpoint of the cluster's ClusterRegistry, as
registry push names it there. An endpoint that does not hold it counts as done, and the command
fails unless every endpoint deleted it or never held it. A tag goes alone; a digest takes the
manifest and the tags that name it. The image's layers stay on disk until chalkctl registry gc.
The command needs an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Admin client file to authenticate with instead of the secrets file. |
| `--connect-to`{ #flag-connect-to } | list of string | — | ENDPOINT=HOST:PORT: reach an endpoint at another address, such as a forwarded port, while still verifying its certificate for the endpoint; may be repeated. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |

## Examples

```sh
chalkctl registry delete docker.io/library/busybox:1.36.1
```

## See also

- [chalkctl registry](chalkctl_registry.md): Manage the images of the cluster registry
