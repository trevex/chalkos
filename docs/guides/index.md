---
title: "Guides"
description: "Longer walks through a whole setup: a cluster on real machines, one without internet access, a release pipeline for your own images, an extension and an IPv6 home lab"
---

# Guides

These pages follow one project from start to end, across the roles of the other sections, and
link to the how-to pages for the details of each step.
[Decisions before you install](../get-started/decisions.md) lists the choices that each guide makes
for you, with what each one costs.

## Read in this order

1. [Build a cluster on real machines](cluster-on-machines.md) installs three machines.
2. [Build an air-gapped cluster](air-gapped.md) builds on it.
3. [Release your own images](release-your-images.md) publishes a cluster's own images.

## All guides

The guides on the BGP extension and the home lab build on the homelab example, an advanced setup
of a router and three Kubernetes nodes on an IPv6 fabric with BGP.

<!-- docgen cards: cluster-on-machines.md air-gapped.md release-your-images.md bgp-extension.md home-lab.md -->
<div class="grid cards" markdown>

-   __[Build a cluster on real machines](cluster-on-machines.md)__

    ---

    Install three machines from the cluster-metal template as a Kubernetes cluster of three control planes behind a VIP

-   __[Build an air-gapped cluster](air-gapped.md)__

    ---

    Run a chalkos cluster without internet access: what to bring in, in which order, and how to upgrade it later

-   __[Release your own images](release-your-images.md)__

    ---

    Build, sign and push a cluster's own images from GitHub Actions on every tag, pin their digests in the inventory and follow chalkos releases

-   __[Write a BGP extension](bgp-extension.md)__

    ---

    Build a BIRD BGP extension from an empty module, test each part, and debug it on a node

-   __[Build a home lab](home-lab.md)__

    ---

    Turn four machines and a switch into a router and a three-node Kubernetes cluster on an IPv6 fabric

</div>
<!-- end docgen cards -->
