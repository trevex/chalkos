---
title: "chalkctl validate"
description: "Check nodes' config against the schemas of their images"
---

# chalkctl validate

```sh
chalkctl validate [<node>...] [flags]
```

Checks the config of the nodes named, or of every node of the inventory, as each node would
check it on its image: the files the inventory gives the node form its set, every document is
checked against the schemas of the node's image in the inventory, its directory's or those of
the contract layer of its image in a registry, whose tag chalkctl resolves to a digest once per
command, and the set is translated as the node translates it. The image's change rules that read
no facts about a node run too, without a running config to compare with: the rules of kinds and
the rules of fields that do not compare with the running value. Before any node, it checks that
every node whose files hold a Kubernetes Cluster document holds the same Cluster spec and the same
Flannel spec or none, as every command that reads the inventory does. It prints a line for each
node whose config is valid, with the digest of an image in a registry, and names each node whose
config is not, and warns of a node whose SystemDisk leaves STATE, which holds its keys and secrets,
unencrypted. It reaches no node and needs no secrets file or client file.

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
