---
title: "chalkctl validate"
description: "Check nodes' config against the schemas of their images"
---

# chalkctl validate

Check nodes' config against the schemas of their images

## Synopsis

Checks the config of the nodes named, or of every node of the inventory, as each node would
check it on its image: the files the inventory gives the node form its set, every document is
checked against the schemas of the image directory the node's Image names, and the set is
translated as the node translates it. It prints a line for each node whose config is valid and
names each node whose config is not. It reaches no node and needs no secrets file or client
file.

```
chalkctl validate [<node>...] [flags]
```

## Examples

```
  # Check every node of chalkos.yaml.
  chalkctl validate

  # Check two nodes of another inventory.
  chalkctl validate cp1 w1 --inventory lab/chalkos.yaml
```

## Options

```
  -h, --help               help for validate
      --inventory string   inventory naming the nodes and their config files (default chalkos.yaml in the current directory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

