---
title: "Get started"
description: "What chalkos is, a first lab cluster and what a node needs"
---

# Get started

These pages are for readers new to chalkos. They explain the model of a cluster, run a lab
cluster or a lab router on one machine and list what real machines need. The
[guides](../guides/index.md) go on to a cluster on real machines.

## Read in this order

1. [What is chalkos](what-is-chalkos.md) explains the model of a cluster.
2. [Quick start: Kubernetes](quick-start-kubernetes.md) runs a lab cluster on one machine.
3. [Requirements](requirements.md) lists what real machines need.
4. [Decisions before you install](decisions.md) lists the choices to make first.

## Understand chalkos

This page explains the model of a cluster before any command.

<!-- docgen cards: what-is-chalkos.md -->
<div class="grid cards" markdown>

-   __[What is chalkos](what-is-chalkos.md)__

    ---

    What chalkos builds, what sets it apart, and the model of a cluster from images and config to an upgrade

</div>
<!-- end docgen cards -->

## Quick starts

Each quick start runs a lab of virtual machines on one machine in about 15 minutes.

<!-- docgen cards: quick-start-kubernetes.md quick-start-router.md -->
<div class="grid cards" markdown>

-   __[Quick start: Kubernetes](quick-start-kubernetes.md)__

    ---

    Run a chalkos cluster of two QEMU virtual machines on this machine with chalklab

-   __[Quick start: router](quick-start-router.md)__

    ---

    Run a chalkos router and a client on its LAN as QEMU virtual machines with chalklab, and change its firewall on trial

</div>
<!-- end docgen cards -->

## Before you install

These pages prepare an install on real machines.

<!-- docgen cards: requirements.md decisions.md releases.md security.md -->
<div class="grid cards" markdown>

-   __[Requirements](requirements.md)__

    ---

    What chalkos needs from machines, firmware, disks and networks, and from the workstation, build host and lab host

-   __[Decisions before you install](decisions.md)__

    ---

    The choices that shape a chalkos cluster before its first install, each with its consequence and the page that carries it out

-   __[Releases](releases.md)__

    ---

    What a chalkos release publishes, when to use its generic images, and how to pin and verify them

-   __[Security policy](security.md)__

    ---

    Which chalkos releases receive security fixes, how to report a vulnerability, and how the project scans its code and images

</div>
<!-- end docgen cards -->
