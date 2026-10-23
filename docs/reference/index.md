---
title: "Reference"
description: "Commands, config kinds, mkImages and image options, the node API and the layout of images, ports and os-release"
---

# Reference

These pages are for anyone who needs an exact name, field, default or port while working with
chalkos.

<div class="grid cards" markdown>

-   :material-console: __Command line__

    ---

    Every command of chalkctl, chalklab, chalkbuild and chalkcfg, from their help.

    [:octicons-arrow-right-24: Read](cli/index.md)

-   :material-code-json: __Config kinds__

    ---

    The fields of every config kind an image accepts, and of the inventory and the lab file.

    [:octicons-arrow-right-24: Read](kinds.md)

-   :material-tune: __mkImages and image options__

    ---

    The arguments of `chalkos.lib.mkImages` and the options of the images it builds.

    [:octicons-arrow-right-24: Read](options.md)

-   :material-api: __Node API__

    ---

    The API chalkd serves on every node, generated from its protobuf definition.

    [:octicons-arrow-right-24: Read](api.md)

-   :material-harddisk: __Image and partition layout__

    ---

    The partitions of an installed node and the files of an image.

    [:octicons-arrow-right-24: Read](image-layout.md)

-   :material-lan: __Ports and firewall__

    ---

    Every port a node listens on, who needs to reach it, and how the firewall treats it.

    [:octicons-arrow-right-24: Read](ports-and-firewall.md)

-   :material-information-outline: __os-release fields__

    ---

    The fields chalkos adds to `/etc/os-release` and the UKI's os-release, and what reads them.

    [:octicons-arrow-right-24: Read](os-release.md)

-   :material-book-alphabet: __Glossary__

    ---

    The terms these pages use, each with a short definition.

    [:octicons-arrow-right-24: Read](glossary.md)

</div>

Sources: the command line, config kinds, options and node API pages are generated from the code,
as [Writing documentation](../contribute/writing-docs.md#generated-pages) describes.
