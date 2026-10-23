---
title: "Build images"
description: "Build, customise, sign and publish chalkos images"
---

# Build images

These pages are for image authors who build a cluster's images in Nix. They start with a first
flake, then cover variants and platforms, added software and drivers, the installer, signing for
Secure Boot and publishing to a registry.

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

-   :material-wrench: __Customise an image__

    ---

    Add packages, services, kernel modules, variants, config kinds and checked service config to a
    cluster's images.

    [:octicons-arrow-right-24: Read](customise-image.md)

-   :material-disc: __Customise the installer__

    ---

    Give the installer static addresses, VLANs, bonds, drivers or firmware.

    [:octicons-arrow-right-24: Read](customise-installer.md)

-   :material-file-certificate: __Sign images for Secure Boot__

    ---

    Create a db signing key, enrol it in the firmware, and sign the images and the installer with
    it.

    [:octicons-arrow-right-24: Read](secure-boot-signing.md)

-   :material-cloud-upload: __Publish images to a registry__

    ---

    Build an image as an OCI artifact, sign it, push it, and name it in the inventory.

    [:octicons-arrow-right-24: Read](publish-images.md)

</div>
