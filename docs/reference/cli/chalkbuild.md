---
title: "chalkbuild"
description: "Prepare the images Nix builds for a chalkos cluster's nodes"
---

# chalkbuild

Prepare the images Nix builds for a chalkos cluster's nodes

## Synopsis

chalkbuild prepares the images Nix builds for a chalkos cluster's nodes, on the machine that
holds the Secure Boot db key. Nix builds images unsigned, so the key never enters the Nix store or
a binary cache; chalkbuild sign signs a copy of an image directory, which the inventory then names
and chalkctl installs and upgrades nodes with. chalkbuild never talks to nodes, and its commands
need no secrets file or client file.

## Examples

```
  nix build .#node-metal -o build/node-metal
  chalkbuild sign --key db.key --cert db.crt build/node-metal images/node-metal
```

## Options

```
  -h, --help   help for chalkbuild
```

## SEE ALSO

* [chalkbuild completion](chalkbuild_completion.md)	 - Generate the autocompletion script for the specified shell
* [chalkbuild sign](chalkbuild_sign.md)	 - Sign a copy of an image directory for Secure Boot

