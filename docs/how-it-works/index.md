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

These pages describe the operating system of a node: the base that every image carries. A layer
adds one role on top of the base, and an image carries one layer at most. The Kubernetes layer
and the router layer below describe what chalkos adds to run Kubernetes on it, or to route
between networks.

<div class="grid cards" markdown>

-   :material-map-outline: __How a node works__

    ---

    The machines that build, drive and run a node, and the stages of a node's life.

    [:octicons-arrow-right-24: Read](node/overview.md)

-   :material-sitemap: __Architecture__

    ---

    The parts of chalkos, where each one runs, and how chalkd picks its mode.

    [:octicons-arrow-right-24: Read](node/architecture.md)

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

-   :material-file-document-edit: __The config pipeline__

    ---

    How a delivery passes a node's documents through rules, outputs and effects, and what happens
    at boot and after an upgrade.

    [:octicons-arrow-right-24: Read](node/config-pipeline.md)

-   :material-database: __Storage and encryption__

    ---

    STATE, VAR and volumes, how they are encrypted and unlocked, and how they change after
    install.

    [:octicons-arrow-right-24: Read](node/storage.md)

-   :material-lan: __Networking__

    ---

    How a node configures its interfaces with networkd, filters traffic with nftables and keeps its
    clock.

    [:octicons-arrow-right-24: Read](node/networking.md)

-   :material-shield-lock: __Security model__

    ---

    What Secure Boot, the TPM, the OS CA and client roles protect, and what they do not.

    [:octicons-arrow-right-24: Read](node/security.md)

-   :material-certificate-outline: __Certificates__

    ---

    The OS CA, the node CA and the node API's certificates, how they are renewed and rotated, and
    why the clock matters.

    [:octicons-arrow-right-24: Read](node/certificates.md)

-   :material-update: __Upgrades__

    ---

    How a new image reaches a node, how the node writes and checks it, and how it boots it on trial.

    [:octicons-arrow-right-24: Read](node/upgrades.md)

</div>

## Kubernetes layer

These pages describe what chalkos adds to a node to run Kubernetes on the shipped variant.

<div class="grid cards" markdown>

-   :material-kubernetes: __How Kubernetes runs on a node__

    ---

    The components of each type of node, the Kubernetes share and where the config comes from.

    [:octicons-arrow-right-24: Read](kubernetes/overview.md)

-   :material-server-network: __The control plane__

    ---

    The static pods, the bootstrap, the joins of etcd and the add-ons the control plane applies.

    [:octicons-arrow-right-24: Read](kubernetes/control-plane.md)

-   :material-lan-connect: __Kubernetes networking__

    ---

    How a node picks its addresses, dual stack, the VIP election, flannel and the ports of the
    control plane.

    [:octicons-arrow-right-24: Read](kubernetes/networking.md)

-   :material-certificate: __Kubernetes certificates__

    ---

    The Kubernetes CAs and keys, the certificates of the control plane and the kubelets, and their
    rotation.

    [:octicons-arrow-right-24: Read](kubernetes/certificates.md)

-   :material-update: __Kubernetes upgrades__

    ---

    How a new Kubernetes version rolls through the control planes first.

    [:octicons-arrow-right-24: Read](kubernetes/upgrades.md)

</div>

## Router layer

These pages describe what chalkos adds to a node of a router image to forward between its
networks and filter what passes.

<div class="grid cards" markdown>

-   :material-router-network: __How a router works__

    ---

    The router kinds, the Router document's kernel settings, the boot order and the guard of the
    management interfaces.

    [:octicons-arrow-right-24: Read](router/overview.md)

-   :material-wall-fire: __The router's firewall__

    ---

    The one nftables table of a router: the protected rules, zones and policies, NAT, the
    reverse-path filter and the fallback ruleset.

    [:octicons-arrow-right-24: Read](router/firewall.md)

-   :material-transit-connection-variant: __BGP on a router__

    ---

    FRR's config of the BGP kinds, the passwords as credentials, the reload, the boot order and
    the state chalkd reads.

    [:octicons-arrow-right-24: Read](router/bgp.md)

-   :material-swap-horizontal: __NAT64 and DNS64 on a router__

    ---

    Jool's instances of the NAT64 documents, the prefix's route, Unbound's rendered config with
    DNS64, the reloads and the state chalkd reads.

    [:octicons-arrow-right-24: Read](router/nat64-and-dns.md)

</div>
