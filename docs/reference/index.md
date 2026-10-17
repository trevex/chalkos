---
title: "Reference"
description: "Commands, config kinds, mkImages and image options, the node API and the layout of images, ports and os-release"
---

# Reference

Most reference pages are generated from the code, so they describe what the code does;
`nix run .#docgen` writes them, and the `docs-generated` check fails when they are out of date.

- [Command line](cli/index.md): every command of `chalkctl`, `chalklab` and `chalkcfg`, from their
  help.
- [Config kinds](kinds.md): the fields of every config kind an image accepts, and of the inventory
  and the lab file, from their schemas.
- [mkImages and image options](options.md): the arguments of `chalkos.lib.mkImages` and the options
  of the images it builds.
- [Node API](api.md): the API chalkd serves on every node.

The other pages are written by hand:

- [Image and partition layout](image-layout.md): the partitions of a node and the files of an image.
- [Ports and firewall](ports-and-firewall.md): what a node listens on and who needs to reach it.
- [os-release fields](os-release.md): the fields chalkos adds to os-release.
- [Glossary](glossary.md): the terms these pages use, each with a short definition.
