---
title: "chalkctl validate"
description: "Check nodes' config against the schemas of their images"
---

# chalkctl validate

```sh
chalkctl validate [<node>...] [flags]
```

Checks the config of the nodes named, or of every node of the inventory, as each node would
check it on its image: the set the inventory gives the node, every document against the schemas of
the node's image in the inventory, and the set's translation. The image's change rules that read no
facts and compare with no running value run too. Every node whose files hold a Kubernetes Cluster
document must hold the same Cluster spec, and the same Flannel spec or none. How chalkos works
describes the checks:
[https://trevex.github.io/chalkos/how-it-works/node/config/#chalkctl-and-chalkd-both-check-the-config](../../how-it-works/node/config.md#chalkctl-and-chalkd-both-check-the-config)

The command prints a line for each node whose config is valid, with the digest of an image in a
registry, names each node whose config is not, and warns of a node whose SystemDisk leaves STATE,
which holds its keys and secrets, unencrypted. It reaches no node and needs no secrets file or
client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory | Inventory naming the nodes and their config files. |

## Examples

```sh
# Check every node of chalkos.yaml.
chalkctl validate

# Check two nodes of another inventory.
chalkctl validate cp1 w1 --inventory lab/chalkos.yaml
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
