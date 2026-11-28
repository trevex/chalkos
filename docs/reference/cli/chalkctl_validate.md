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
facts and compare with no running value run too, and so does the validator router of a router
image, which chalkctl links. Every node whose files hold a Kubernetes Cluster document must hold
the same Cluster spec, and the same Flannel spec or none. A valid node is also refused when its
config disagrees with another node of the inventory on a field that either node's image marks as
held alike by every node of a cluster; a node whose config is invalid is compared with none. A node
is compared with another node whose image cannot be read only on the fields its own image marks,
with a warning. See
[chalkctl and chalkd both check the config](../../how-it-works/node/config.md#chalkctl-and-chalkd-both-check-the-config).

The command prints a line for each node whose config is valid, with the digest of an image in a
registry, names each node whose config is not, and warns of a node whose SystemDisk leaves STATE,
which holds its keys and secrets, unencrypted, and of each image reference that names a tag but no
digest, which nodes pull by whatever the tag points to when they pull. A layer warns of what the
nodes' config together leaves open, such as flannel's VXLAN taken from any source, with the
ranges of the nodes' addresses to set instead. It reaches no node.

validate reads the file:// sources of Secret documents and decrypts no sops:// or age:// source,
so it asks for no key. It checks each key of such a source with a placeholder value, which no
rule or validator sees, as they see a Secret's values as hashes alone. It warns of each such key
and prints no config version, which hashes the values: whether the source decrypts and holds the
value shows when a command that delivers the config reads it.

A config validate passes can still be refused on delivery: validate runs no rule that reads the
node's facts or its running config, none of the image's other validators, and none of its
renderers or their checks. chalkctl config apply --dry-run has the node run all of them, and
changes nothing.

-o json prints the result as an object: nodes holds each node's node, valid, error when it is not,
the image it was checked against as image, imageReference, imageName, platform and rootHash, its
configVersion and its warnings; warnings holds the layers' warnings and those of the nodes compared
with a node whose image cannot be read. It exits as the lines do.

The command needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory | Inventory naming the nodes and their config files. |
| `--output`{ #flag-output }, `-o` | format | `text` | Format to print: text, or json for programs. |

## Examples

```sh
# Check every node of chalkos.yaml.
chalkctl validate

# Check two nodes of another inventory.
chalkctl validate cp1 w1 --inventory lab/chalkos.yaml
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
