---
title: "Build images"
description: "Customise, sign and publish chalkos images"
---

# Build images

These pages are for image authors who build a cluster's images in Nix. They cover customising the
images and the installer, adding hardware support, signing for Secure Boot and publishing to a
registry.

<div class="grid cards" markdown>

-   :material-wrench: __Customise an image__

    ---

    Add packages, services, kernel modules, variants, config kinds and checked service config to a
    cluster's images.

    [:octicons-arrow-right-24: Read](customise-image.md)

-   :material-disc: __Customise the installer__

    ---

    Give the installer static addresses, VLANs, bonds, drivers or firmware.

    [:octicons-arrow-right-24: Read](customise-installer.md)

-   :material-expansion-card: __Support additional hardware__

    ---

    Add drivers, firmware and platform settings for hardware the base images do not cover.

    [:octicons-arrow-right-24: Read](additional-hardware.md)

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
