---
title: "Reference"
description: "Commands, config kinds, image options, built-ins, the node API, boot units, roles, certificates, Kubernetes, ports and os-release"
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

-   :material-tune: __mkImages__

    ---

    The arguments of `chalkos.lib.mkImages` and what it returns.

    [:octicons-arrow-right-24: Read](mkimages.md)

-   :material-cog: __Image options__

    ---

    The options of chalkos's node modules that a variant's NixOS modules set.

    [:octicons-arrow-right-24: Read](image-options.md)

-   :material-puzzle: __Extension API__

    ---

    The options and `lib.chalkos` helpers an extension declares kinds, facts, outputs and effects with.

    [:octicons-arrow-right-24: Read](extension-api.md)

-   :material-format-list-checks: __Built-ins__

    ---

    The change rules, facts, outputs and effects that chalkos's own modules declare.

    [:octicons-arrow-right-24: Read](builtins.md)

-   :material-api: __Node API__

    ---

    The API chalkd serves on every node, generated from its protobuf definition.

    [:octicons-arrow-right-24: Read](api.md)

-   :material-harddisk: __Image and partition layout__

    ---

    The partitions of an installed node and the files of an image.

    [:octicons-arrow-right-24: Read](image-layout.md)

-   :material-restart: __Boot and the image__

    ---

    How systemd-boot orders a node's entries, the units that run at every boot, and what an image fixes.

    [:octicons-arrow-right-24: Read](boot.md)

-   :material-account-key: __Roles and credentials__

    ---

    The role each node API method needs, and the order chalkctl looks for credentials.

    [:octicons-arrow-right-24: Read](access.md)

-   :material-certificate-outline: __Certificates__

    ---

    Every CA, key and certificate of a cluster, its lifetime, its renewal point and the status notes.

    [:octicons-arrow-right-24: Read](certificates.md)

-   :material-kubernetes: __Kubernetes__

    ---

    The components of each node type, the flags chalkos refuses, its add-ons and the status states.

    [:octicons-arrow-right-24: Read](kubernetes.md)

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

Sources: the command line, config kinds, mkImages, image options, extension API, built-ins and node
API pages are generated from the code, as
[Writing documentation](../contribute/writing-docs.md#generated-pages) describes.
