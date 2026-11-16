---
title: "Guides"
description: "Longer walks through a whole setup: a home lab on real machines and an extension built from scratch"
---

# Guides

These pages follow one project from start to end, across the roles of the other sections. Each
builds on the homelab example, a router and three Kubernetes nodes on real machines, and links to
the how-to pages for the details of each step.
[Decisions before you install](../get-started/decisions.md) lists the choices that each guide makes
for you, with what each one costs.

<div class="grid cards" markdown>

-   :material-home-variant-outline: __Build a home lab__

    ---

    Turn four machines and a switch into a router and three Kubernetes control planes on an IPv6
    fabric, and test what happens when a cable or a node fails.

    [:octicons-arrow-right-24: Build](home-lab.md)

-   :material-puzzle-outline: __Write a BGP extension__

    ---

    Build the homelab's bird extension from an empty module: its kinds, the rendered config, the
    service, effects, rules, a validator, a diagnostic and their tests, and debug it on a node.

    [:octicons-arrow-right-24: Write](bgp-extension.md)

</div>
