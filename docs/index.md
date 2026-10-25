---
icon: lucide/layers
title: "chalkos"
description: "An image-based NixOS for nodes that run as appliances"
---

<div class="ck-hero" markdown>

# chalkos

<p class="ck-tagline" markdown>
chalkos is an image-based NixOS for nodes that run as appliances, today as Kubernetes nodes and
routers.
Nix builds the images, and Secure Boot verifies them at every boot.
</p>

[Get started :material-arrow-right:](get-started/quick-start.md){ .md-button .md-button--primary }
[What is chalkos](get-started/what-is-chalkos.md){ .md-button }

</div>

A machine that is changed in place drifts. Packages, hand edits and half-done upgrades pile up
until nobody can say what a node runs. A failed upgrade leaves a machine that someone repairs at
the console. chalkos changes a node only by replacing its whole image. Each node boots the exact
image that was built and signed, and it has no login through which anyone could change it.

A Nix flake calls `mkImages`, which builds one image per variant and platform from NixOS
modules. An image holds a read-only Nix store on dm-verity and a unified kernel image (UKI) that
Secure Boot verifies. YAML documents in the style of Kubernetes objects configure each node.
chalkctl delivers the documents and the images to the nodes and never runs Nix. chalkd, the agent
on each node, applies the config and writes an upgrade into the inactive one of two A/B slots.
When the new image does not boot healthy, the node returns to the image before.

## What chalkos is

<div class="grid" markdown>

:material-shield-check: __Verified from build to boot__
{ .card }

Secure Boot verifies the signed UKI, and the UKI pins the root hash of the store. dm-verity then
checks every block the node reads against what Nix built. The TPM unseals the node's encrypted
state only under the same Secure Boot state.

:material-backup-restore: __Appliance nodes__
{ .card }

Nodes have no SSH and no login. chalkd serves an API over mutual TLS, and the role in a client's
certificate decides what it may call. A node boots a new image with a fixed number of tries and
returns to the image before when none of them comes up healthy.

:material-puzzle: __Extend with NixOS modules__
{ .card }

A variant is a set of ordinary NixOS modules, so it can add any software, driver or kernel
setting. A module can also declare a config kind of its own, render a service's config files
from it, and reload or restart the service when those files change.

</div>

## Explore the docs

<div class="grid cards" markdown>

-   :material-rocket-launch: __Get started__

    ---

    What chalkos is, and a lab cluster of two virtual machines on one machine. Then what real
    machines need.

    [:octicons-arrow-right-24: Start](get-started/index.md)

-   :material-server: __Operate nodes__

    ---

    Install, upgrade, recover and troubleshoot nodes with chalkctl and YAML files. The machine
    that runs chalkctl needs no Nix.

    [:octicons-arrow-right-24: Operate](operate/index.md)

-   :material-kubernetes: __Run Kubernetes__

    ---

    Plan a Kubernetes cluster on chalkos nodes and run its control plane on three nodes behind a
    virtual IP.

    [:octicons-arrow-right-24: Run](kubernetes/index.md)

-   :material-router-network: __Run a router__

    ---

    Forward between a node's networks, filter and translate what passes with zones, policies
    and NAT rules, and exchange routes over BGP.

    [:octicons-arrow-right-24: Run](router/index.md)

-   :material-package-variant-closed: __Build images__

    ---

    Customise images and the installer with NixOS modules, then sign them for Secure Boot and
    publish them to a registry.

    [:octicons-arrow-right-24: Build](build/index.md)

-   :material-cog-outline: __How chalkos works__

    ---

    How a node is built, configured, booted, secured and upgraded, and how chalkos runs
    Kubernetes on those nodes.

    [:octicons-arrow-right-24: Learn](how-it-works/index.md)

-   :material-book-open-variant: __Reference__

    ---

    Commands, config kinds, `mkImages` and image options, the node API, ports, the image layout
    and the glossary.

    [:octicons-arrow-right-24: Look up](reference/index.md)

-   :material-source-pull: __Contribute__

    ---

    The development setup, the tests, the layout of the code and how these pages are written.

    [:octicons-arrow-right-24: Contribute](contribute/index.md)

</div>

!!! note "Status"

    chalkos is unreleased. Until a first release, the config kinds, all at `v1alpha1`, the
    `mkImages` arguments, the image options, the commands and the node API change without
    migrations. A cluster may need its YAML or flake edited, or its nodes reinstalled, when it
    moves to a newer chalkos. Run it in labs and test clusters.
