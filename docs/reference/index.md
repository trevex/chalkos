---
title: "Reference"
description: "Commands, options, the node API and the layout of images, ports and os-release"
---

# Reference

<!--
Scope: An overview of the reference pages: which are generated from the code and how, and
which are written by hand.
-->

Most reference pages are generated from the code, so they describe what the code does;
`nix run .#docgen` writes them, and the `docs-generated` check fails when they are out of date.

- [Command line](cli/index.md): every command of `chalkctl` and `chalklab`, from their help.
- [Cluster and node options](options.md): the options of a cluster definition and of role images.
- [Node API](api.md): the API chalkd serves on every node.

The other pages are written by hand:

- [Image and partition layout](image-layout.md): the partitions of a node and the files of an image.
- [Ports and firewall](ports-and-firewall.md): what a node listens on and who needs to reach it.
- [os-release fields](os-release.md): the fields chalkos adds to os-release.
- [Glossary](glossary.md): the terms these pages use, each with a short definition.
