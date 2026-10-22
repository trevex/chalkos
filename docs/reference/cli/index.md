---
title: "Command line"
description: "The commands of chalkctl, chalklab, chalkbuild and chalkcfg"
---

# Command line

chalkos has four command line tools: chalkctl, chalklab and chalkbuild run on an operator's machine, chalkcfg on a node. Their pages are generated from their help.

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
- [chalkbuild](chalkbuild.md): Prepare the images Nix builds for a chalkos cluster's nodes
- [chalkcfg](chalkcfg.md): Print the config documents of the node chalkcfg runs on

Every command shows its description, examples and flags with `--help`. chalkctl's, chalklab's and chalkbuild's `completion` writes a shell completion script.
