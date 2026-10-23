---
title: "How chalkos works"
description: "How chalkos works and why it works that way"
---

# How chalkos works

These pages are for operators, image authors and contributors who want the mechanism behind a
task. They explain how a node is built, configured, booted, secured and upgraded, and where each
protection stops. For the whole model on one page first, read
[What is chalkos](../get-started/what-is-chalkos.md).

## The node OS

These pages describe the operating system of a node. The Kubernetes layer below describes what
chalkos adds to run Kubernetes on it.

<div class="grid cards" markdown>

-   :material-sitemap: __Architecture__

    ---

    The parts of chalkos, and how images from Nix and YAML config become installed, running
    nodes.

    [:octicons-arrow-right-24: Read](node/architecture.md)

-   :material-layers-triple: __Extensions__

    ---

    How an extension adds a config kind and the services that read its documents to a variant's
    images.

    [:octicons-arrow-right-24: Read](node/variants.md)

-   :material-harddisk: __The image__

    ---

    What an image holds: the erofs store on dm-verity, the signed UKI and the A/B slots.

    [:octicons-arrow-right-24: Read](node/image.md)

-   :material-restart: __Boot, health and rollback__

    ---

    How a node boots, decides whether a boot is healthy, and falls back from an image that is not.

    [:octicons-arrow-right-24: Read](node/boot-and-rollback.md)

-   :material-file-cog: __Config and the inventory__

    ---

    How YAML config documents describe each node, how the inventory gives them out, and how a node
    applies them.

    [:octicons-arrow-right-24: Read](node/config.md)

-   :material-check-decagram: __Change rules and facts__

    ---

    How an image's CEL rules decide which config changes a node accepts, and the node state they
    read.

    [:octicons-arrow-right-24: Read](node/rules.md)

-   :material-file-document-edit: __Outputs and effects__

    ---

    How a node renders and checks its services' config files before a delivery changes anything,
    and which deliveries reload or restart a service.

    [:octicons-arrow-right-24: Read](node/outputs-and-effects.md)

-   :material-database: __Storage and encryption__

    ---

    STATE, VAR and volumes, how they are encrypted and unlocked, and how they change after
    install.

    [:octicons-arrow-right-24: Read](node/storage.md)

-   :material-lan: __Networking__

    ---

    Node addresses, dual stack, the virtual IP, and packet filtering with nftables.

    [:octicons-arrow-right-24: Read](node/networking.md)

-   :material-shield-lock: __Security model__

    ---

    What Secure Boot, the TPM, the OS CA and client roles protect, and what they do not.

    [:octicons-arrow-right-24: Read](node/security.md)

-   :material-certificate-outline: __Certificates__

    ---

    Every certificate authority and certificate of a cluster, and how each is renewed and rotated.

    [:octicons-arrow-right-24: Read](node/certificates.md)

-   :material-update: __Upgrades__

    ---

    How a new image reaches a node, how chalkctl rolls it through a cluster, and what stops it.

    [:octicons-arrow-right-24: Read](node/upgrades.md)

</div>

## Kubernetes layer

<div class="grid cards" markdown>

-   :material-kubernetes: __Kubernetes on chalkos__

    ---

    How chalkos runs etcd, the control plane, the kubelet and the cluster's add-ons.

    [:octicons-arrow-right-24: Read](kubernetes/kubernetes.md)

</div>
