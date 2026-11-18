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
fails unless every endpoint deleted it or never held it.

A tag goes alone: chalkctl prints the digest it named and the tags that still name it. When none
do, the manifest stays, and with it its layers, until chalkctl registry delete
&lt;repository>@&lt;digest>. A digest takes the manifest. Before it deletes anything, chalkctl refuses a
digest that tags name unless --with-tags is given, and then prints each tag it deleted with it;
it always refuses a digest that a tagged index names, as the index would break. The layers stay
on disk until chalkctl registry gc. The command needs an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Admin client file to authenticate with instead of the secrets file. |
| `--connect-to`{ #flag-connect-to } | list of string | — | ENDPOINT=HOST:PORT: reach an endpoint at another address, such as a forwarded port, while still verifying its certificate for the endpoint; may be repeated, once per endpoint. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |
| `--with-tags`{ #flag-with-tags } | boolean | `false` | Delete a digest together with the tags that name it, which the command refuses otherwise. |

## Examples

```sh
# Take the tag away; the manifest stays.
chalkctl registry delete docker.io/library/busybox:1.36.1

# Delete the manifest and every tag that names it.
chalkctl registry delete docker.io/library/busybox@sha256:7b3ccabffc97de872a30dfd234fd972a66d247c8cfc69b0550f276481852627c --with-tags
```

## See also

- [chalkctl registry](chalkctl_registry.md): Manage the images of the cluster registry
