---
title: "Build images"
description: "Build, customise, sign and publish chalkos images"
---

# Build images

These pages are for image authors who build a cluster's images in Nix with
`chalkos.lib.mkImages`. They assume some Nix and NixOS modules, and no earlier knowledge of chalkos.

Start here: [Your first image flake](first-image.md), then
[Variants and platforms](variants-and-platforms.md), then
[Add software, drivers and kernel modules](add-software.md). Every later page builds on the
flake from these three.

## Build

These pages build an image a node can install: the flake, its variants and platforms, and the
software it carries.

<div class="grid cards" markdown>

-   :material-rocket-launch: __Your first image flake__

    ---

    Write a flake that builds a cluster's image with mkImages, into the directory the inventory
    names.

    [:octicons-arrow-right-24: Read](first-image.md)

-   :material-layers: __Variants and platforms__

    ---

    Build images for nodes that run other software or other machines, and set the order of their
    modules.

    [:octicons-arrow-right-24: Read](variants-and-platforms.md)

-   :material-expansion-card: __Add software, drivers and kernel modules__

    ---

    Add packages, services, kernel modules, firmware and debug tools within an appliance's limits.

    [:octicons-arrow-right-24: Read](add-software.md)

</div>

## Configure services

These pages give the services in an image their configuration: a config kind for each node's
YAML, the files and secrets a unit reads, and the effects and rules that apply when a delivery
changes them.

<div class="grid cards" markdown>

-   :material-shape-plus: __Declare a config kind__

    ---

    Declare a config kind in a NixOS module, so each node's YAML carries values the image checks.

    [:octicons-arrow-right-24: Read](declare-a-kind.md)

-   :material-file-search: __Read config in a service__

    ---

    Grant a unit the config kinds it reads, and read their documents as files or with chalkcfg.

    [:octicons-arrow-right-24: Read](read-config.md)

-   :material-file-code: __Render config files__

    ---

    Render a service's config file from the node's documents, checked before a delivery changes
    anything.

    [:octicons-arrow-right-24: Read](render-config.md)

-   :material-refresh: __React to changes__

    ---

    Restart, reload or run a command when a delivery changes an output, a kind's documents or a
    condition.

    [:octicons-arrow-right-24: Read](react-to-changes.md)

-   :material-key-chain: __Pass secrets to a service__

    ---

    Reference a Secret from a field of your kind, so a granted unit gets its value as a systemd
    credential.

    [:octicons-arrow-right-24: Read](pass-secrets.md)

-   :material-shield-check: __Guard changes with rules and facts__

    ---

    Refuse a config change a node cannot make, with CEL rules on your kind and facts about the
    node.

    [:octicons-arrow-right-24: Read](guard-changes.md)

</div>

## Ship

These pages take an image to the nodes: the installer, signing for Secure Boot, publishing to a
registry, and testing and rolling out an upgrade.

<div class="grid cards" markdown>

-   :material-disc: __Customise the installer__

    ---

    Give the installer static addresses, VLANs, bonds, drivers or firmware.

    [:octicons-arrow-right-24: Read](customise-installer.md)

-   :material-file-certificate: __Sign images for Secure Boot__

    ---

    An optional step: create a db key, sign a copy of each image and the installer, and name the
    signed copy in the inventory.

    [:octicons-arrow-right-24: Read](sign-images.md)

-   :material-cloud-upload: __Publish images to a registry__

    ---

    Build an image as an OCI layout, sign it if the nodes need it, push it and name its digest in
    the inventory.

    [:octicons-arrow-right-24: Read](publish-images.md)

-   :material-check-decagram: __Test and roll out an image__

    ---

    Check an image and the nodes' config, upgrade the nodes, and confirm their outputs and
    services.

    [:octicons-arrow-right-24: Read](test-and-roll-out.md)

</div>
