---
icon: lucide/layers
title: "Home"
description: "A toolkit that builds API-driven, immutable Linux distributions from NixOS modules"
---

<div class="chalkos-hero" markdown="1">
<div class="chalkos-hero__content" markdown="1">

# chalkos

<p class="chalkos-hero__tagline" markdown>
chalkos builds API-driven, immutable Linux distributions from NixOS modules. It ships layers for
Kubernetes nodes and routers, and its images carry the schemas of the config they accept. Each
release publishes signed OCI images of both layers that you can run as they are.
</p>

[Get started :material-arrow-right:](get-started/quick-start-kubernetes.md){ .md-button .md-button--primary }
[What is chalkos](get-started/what-is-chalkos.md){ .md-button }

</div>
</div>

A machine that is changed in place drifts. Packages, hand edits and half-done upgrades pile up
until nobody can say what a node runs. [Talos Linux](https://www.talos.dev/) answers this for
Kubernetes with a read-only image, no logins and an API that manages each node. chalkos takes that
operating model and makes the distribution itself something an image author builds. An image is a
set of NixOS modules: a layer that chalkos ships, such as Kubernetes or the router, the services
and drivers the nodes need, and config kinds of the image's own. Each node boots the exact image
that was built and signed, and changes only through its API.

## Choose your path

Start with the path that matches your work. Each one assumes the one before it, so an image
author knows the quick start and a contributor knows both. Each release publishes signed OCI
images for clusters that need no image of their own, as [Releases](get-started/releases.md)
describes, and the quick starts run them. A cluster that needs its own OS CA, Secure Boot keys,
extensions or packages builds its images with Nix.

<div class="grid cards" markdown>

-   :material-server: __Use chalkos__

    ---

    For operators. Run a lab cluster of two virtual machines on one machine in about 15 minutes,
    or [a lab router](get-started/quick-start-router.md). Then take the cluster to real machines
    with [Build a cluster on real machines](guides/cluster-on-machines.md), or do one task at a
    time from [Operate nodes](operate/index.md).

    [:octicons-arrow-right-24: Quick start](get-started/quick-start-kubernetes.md)

-   :material-puzzle-outline: __Customise images__

    ---

    For image authors. Write [your first image flake](build/first-image.md), then
    [your first extension](build/first-extension.md) in about 15 minutes on the lab. The rest of
    [Build images](build/index.md) covers one task per page.

    [:octicons-arrow-right-24: First image flake](build/first-image.md)

-   :material-source-pull: __Contribute__

    ---

    For contributors to chalkos itself. Enter the development shell, run one package's tests and
    commit a first change in about 10 minutes.

    [:octicons-arrow-right-24: First change](contribute/development.md#first-change-in-10-minutes)

</div>

A Nix flake calls `mkImages`, which builds each image the flake names, for its platform, from NixOS
modules. An image holds a read-only Nix store on dm-verity, a unified kernel image (UKI) that
Secure Boot verifies, and the JSON Schemas of its config kinds. YAML documents in the style of
Kubernetes objects configure each node. chalkctl checks them against the node's image, then
delivers the documents and the images to the nodes, and never runs Nix. chalkd, the agent on each
node, applies the config and writes an upgrade into the inactive one of two A/B slots. When the new
image does not boot healthy, the node returns to the image before.

## What chalkos is

<div class="chalkos-overview" markdown>

:material-hammer-wrench: A distribution builder
:   An image is built from ordinary NixOS modules and the packages of nixpkgs, so it can add any
    software, driver or kernel setting. The Kubernetes layer makes a node much like a Talos node; the
    router layer makes it a router in the manner of VyOS, managed the same way.

:material-file-check: Config the image checks
:   A module declares a config kind as Kubernetes declares a custom resource: fields, defaults and
    CEL rules. The image carries the kinds' schemas, so chalkctl refuses a mistake against the image
    a node runs before the node sees it.

:material-swap-horizontal: Config per image
:   A node keeps a config slot for each image it holds. An upgrade brings config that the new image
    checks itself, while the image before keeps its own, so a kind can change between images and a
    rollback still boots with config it understands. [Change a kind incompatibly](build/change-a-kind.md)
    shows such a change.

:material-shield-check: Verified from build to boot
:   Secure Boot verifies the signed UKI, and the UKI pins the root hash of the store. dm-verity then
    checks every block the node reads against what Nix built. The TPM unseals the node's encrypted
    state only under the same Secure Boot state.

:material-lock-check: Nothing runs that the image did not bring
:   Nodes carry no shell, every writable file system is mounted noexec, and the kernel loads
    modules only from the signed image. The [security model](how-it-works/node/security.md) lists
    what each attacker position reaches.

:material-backup-restore: Appliance nodes
:   Nodes have no SSH and no login. chalkd serves an API over mutual TLS, and the role in a client's
    certificate decides what it may call. A node boots a new image with a fixed number of tries and
    returns to the image before when none of them comes up healthy.

:material-test-tube: A toolkit to extend and test
:   An extension renders a service's config files from its kinds, reloads the service on a change and
    adds diagnostics. Its tests run in `nix flake check`, and chalklab runs a whole cluster as virtual
    machines on one host.

</div>

## Explore the docs

<div class="grid cards" markdown>

-   :material-rocket-launch: __Get started__

    ---

    What chalkos is, and a lab cluster of two virtual machines on one machine. Then what real
    machines need.

    [:octicons-arrow-right-24: Start](get-started/index.md)

-   :material-map-marker-path: __Guides__

    ---

    Whole projects from start to end: a cluster on three real machines, a BGP extension written
    from scratch, and an IPv6 home lab with a router.

    [:octicons-arrow-right-24: Follow](guides/index.md)

-   :material-server: __Operate nodes__

    ---

    Install, upgrade, recover and troubleshoot nodes with chalkctl and YAML files. The machine
    that runs chalkctl needs no Nix. Take the generic images of a release, or build images from
    a template or an example with `nix build` first.

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

    chalkos is before 1.0. Until then, the config kinds, all at `v1alpha1`, the `mkImages`
    arguments, the image options, the commands and the node API change between releases without
    migrations. A cluster may need its YAML or flake edited, or its nodes reinstalled, when it
    moves to a newer chalkos. Run it in labs and test clusters.
