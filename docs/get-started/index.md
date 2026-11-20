---
title: "Get started"
description: "What chalkos is, a first lab cluster and what a node needs"
---

# Get started

These pages are for readers new to chalkos. They explain the model of a cluster, run a lab
cluster or a lab router on one machine and list what real machines need. The
[guides](../guides/index.md) go on to a cluster on real machines.

## Understand chalkos

This page explains the model of a cluster before any command.

<div class="grid cards" markdown>

-   :material-lightbulb-on-outline: __What is chalkos__

    ---

    What chalkos builds and what sets it apart, the model of a cluster from the images and the
    config documents to an upgrade, and how chalkos compares with Talos and NixOS.

    [:octicons-arrow-right-24: Read](what-is-chalkos.md)

</div>

## Quick starts

Each quick start runs a lab of virtual machines on one machine in about 15 minutes.

<div class="grid cards" markdown>

-   :material-rocket-launch: __Quick start: Kubernetes__

    ---

    Run a cluster of two QEMU virtual machines on one machine with chalklab, upgrade it and
    remove it, in about 15 minutes.

    [:octicons-arrow-right-24: Start](quick-start-kubernetes.md)

-   :material-router-network: __Quick start: router__

    ---

    Run a router and a client on its LAN as QEMU virtual machines, check its DHCP, DNS and
    masquerade, and change its firewall on trial, in about 15 minutes.

    [:octicons-arrow-right-24: Start](quick-start-router.md)

</div>

## Before you install

These pages prepare an install on real machines.

<div class="grid cards" markdown>

-   :material-clipboard-check-outline: __Requirements__

    ---

    What chalkos needs from machines, firmware, disks and networks, and what a lab needs from its
    host.

    [:octicons-arrow-right-24: Read](requirements.md)

-   :material-source-branch: __Decisions before you install__

    ---

    Secure Boot, the control planes, the VIP, the secrets file and the platform: each choice
    with its consequence.

    [:octicons-arrow-right-24: Read](decisions.md)

-   :material-tag-outline: __Releases__

    ---

    The images, the installer and chalkctl a release publishes, when to use its generic images,
    and how to pin and verify them.

    [:octicons-arrow-right-24: Read](releases.md)

-   :material-shield-check-outline: __Security__

    ---

    Which releases receive security fixes, how to report a vulnerability, and how chalkos scans
    its code and images.

    [:octicons-arrow-right-24: Read](security.md)

</div>
