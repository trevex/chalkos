---
title: "Reference"
description: "Commands, config kinds, built-ins, images, the node API, roles, certificates, ports, Kubernetes and the glossary"
---

# Reference

These pages are for operators, image authors and contributors who look up an exact name, field,
default or port while they work with chalkos. Each page opens with who uses it and closes with
the sources its facts come from.

## Read in this order

1. [Command cheat sheet](cheat-sheet.md) lists the command and the credential for each task.
2. [Config kinds](kinds.md) lists every field of the documents a node takes.
3. [Image options](image-options.md) lists every option of an image's modules.
4. [Glossary](glossary.md) defines the terms every page uses.

## Commands

chalkctl installs and operates a cluster, chalklab runs its nodes as virtual machines, chalkbuild
prepares its images and chalkcfg prints a node's config documents on the node.

<!-- docgen cards: cheat-sheet.md cli/index.md -->
<div class="grid cards" markdown>

-   __[Command cheat sheet](cheat-sheet.md)__

    ---

    The chalkctl and chalklab command for each common task, and the credential each one needs

-   __[Command line](cli/index.md)__

    ---

    The commands of chalkctl, chalklab, chalkbuild and chalkcfg

</div>
<!-- end docgen cards -->

## Config

These pages list what an operator writes in a node's config, and what chalkos's own modules check
and do with it.

<!-- docgen cards: kinds.md builtins.md -->
<div class="grid cards" markdown>

-   __[Config kinds](kinds.md)__

    ---

    The fields of the config kinds of chalkos's images, of the inventory and of the lab file

-   __[Built-ins](builtins.md)__

    ---

    The change rules, facts, validators, outputs, effects and diagnostics that chalkos's images declare

</div>
<!-- end docgen cards -->

## Images

These pages list how an image is built, what it holds and how a node boots it.

<!-- docgen cards: mkimages.md image-options.md extension-api.md extension-runtime.md image-layout.md boot.md os-release.md -->
<div class="grid cards" markdown>

-   __[mkImages](mkimages.md)__

    ---

    The arguments of chalkos.lib.mkImages, mkImage and mkInstaller, what they return, the helpers over their result and the layers

-   __[Image options](image-options.md)__

    ---

    The options of chalkos's node modules that an image's NixOS modules set

-   __[Extension API](extension-api.md)__

    ---

    The options and chalkosLib helpers that an extension declares kinds, facts, outputs, validators, effects and diagnostics with

-   __[Extension runtime](extension-runtime.md)__

    ---

    What an extension's rules, templates and programs read, the functions they call, the limits they run within and the errors they show

-   __[Image and partition layout](image-layout.md)__

    ---

    The partitions of an installed node and the files of an image

-   __[Boot and the image](boot.md)__

    ---

    How systemd-boot orders a node's entries, the units that run at every boot, and what an image fixes

-   __[os-release fields](os-release.md)__

    ---

    The fields chalkos adds to /etc/os-release and the UKI's os-release, and what reads them

</div>
<!-- end docgen cards -->

## Node

These pages list how a node is reached: its API, who may call it, the certificates behind it and
the ports it opens.

<!-- docgen cards: api.md access.md certificates.md ports-and-firewall.md -->
<div class="grid cards" markdown>

-   __[Node API](api.md)__

    ---

    The API chalkd serves on every node, generated from its protobuf definition

-   __[Roles and credentials](access.md)__

    ---

    The role each node API method needs, the modes it is served in, and the order chalkctl looks for credentials

-   __[Certificates](certificates.md)__

    ---

    Every CA, key and certificate of a cluster, with its lifetime, its renewal point and the status notes about it

-   __[Ports and firewall](ports-and-firewall.md)__

    ---

    Every port a chalkos node listens on, who needs to reach it, and how the firewall treats it

</div>
<!-- end docgen cards -->

## Kubernetes

This page lists what the Kubernetes layer adds to run Kubernetes on a node.

<!-- docgen cards: kubernetes.md -->
<div class="grid cards" markdown>

-   __[Kubernetes](kubernetes.md)__

    ---

    The components of each node type, the flags chalkos refuses, the add-ons it applies and the Kubernetes status states

</div>
<!-- end docgen cards -->

## Glossary

The glossary defines the terms every page uses, and is the source of their tooltips.

<!-- docgen cards: glossary.md -->
<div class="grid cards" markdown>

-   __[Glossary](glossary.md)__

    ---

    The terms of the chalkos documentation, each with a short definition and the page that explains it

</div>
<!-- end docgen cards -->

Sources: the command line, config kinds, built-ins, mkImages, image options, extension API and
node API pages are generated from the code, as
[Writing documentation](../contribute/writing-docs.md#generated-pages) describes.
