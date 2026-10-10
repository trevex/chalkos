---
title: "chalkos"
description: "An image-based NixOS for Kubernetes nodes"
---

# chalkos

<!--
Scope: What chalkos is, who it is for, how it compares with Talos and plain NixOS, and where to
start reading.
-->

chalkos turns bare-metal machines and virtual machines into a Kubernetes cluster that runs an
immutable operating system managed through an API. A cluster is declared in a Nix flake; each
role of the cluster becomes a disk image with an erofs Nix store on dm-verity and a unified
kernel image that Secure Boot can verify. Nodes keep their state on TPM-sealed encrypted
partitions, upgrade between A/B slots and fall back when a new image does not boot healthy.
`chalkd` on each node serves the API; `chalkctl` installs and operates the nodes, and
`chalklab` runs a whole cluster as QEMU virtual machines on one machine.

!!! note "This documentation is being written"

    The sections below are outlines of what they will hold. The reference for
    [`chalkctl` and `chalklab`](reference/index.md), the cluster and node options and the node API
    is generated from the code.

## Where to start

- [Getting started](getting-started/index.md): a cluster on this machine with `chalklab`.
- [Concepts](concepts/index.md): how chalkos works and why.
- [Guides](guides/index.md): installing, upgrading and operating real clusters.
- [Reference](reference/index.md): commands, options and the node API.
- [Contributing](contributing/index.md): developing chalkos and its documentation.
