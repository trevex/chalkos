---
title: "Guides"
description: "Longer walks through a whole setup: a cluster on real machines, one without internet access, an extension and an IPv6 home lab"
---

# Guides

These pages follow one project from start to end, across the roles of the other sections, and
link to the how-to pages for the details of each step. Start with the cluster on real machines.
The air-gapped cluster builds on it. The other two build on the homelab example, an advanced setup
of a router and three Kubernetes nodes on an IPv6 fabric with BGP.
[Decisions before you install](../get-started/decisions.md) lists the choices that each guide makes
for you, with what each one costs.

<div class="grid cards" markdown>

-   :material-server-network: __Build a cluster on real machines__

    ---

    Install three machines from the `cluster-metal` template as a Kubernetes cluster of three
    control planes, with static IPv4 addresses and a VIP for the Kubernetes API.

    [:octicons-arrow-right-24: Build](cluster-on-machines.md)

-   :material-wifi-off: __Build an air-gapped cluster__

    ---

    Run the cluster without internet access: bring in the OS images, chalkctl and the container
    images from a connected machine, serve them from the control planes and keep time locally.

    [:octicons-arrow-right-24: Build](air-gapped.md)

-   :material-puzzle-outline: __Write a BGP extension__

    ---

    Build a BIRD extension from an empty module: its kinds, the rendered config, the
    service, effects, rules, a validator, a diagnostic and their tests, and debug it on a node.

    [:octicons-arrow-right-24: Write](bgp-extension.md)

-   :material-home-variant-outline: __Build a home lab__

    ---

    An advanced setup: turn four machines and a switch into a router and three Kubernetes
    control planes on an IPv6 fabric with BGP, and test what happens when a cable or a node fails.

    [:octicons-arrow-right-24: Build](home-lab.md)

</div>
