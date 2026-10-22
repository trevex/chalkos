---
title: "Concepts"
description: "How chalkos works and why it works that way"
---

# Concepts

These pages explain how chalkos works and why it is built the way it is: how Nix builds a
cluster's signed images, how YAML documents configure each node, how a node boots, unlocks and
falls back from a bad image, and how it runs Kubernetes. They describe mechanisms and their limits
rather than steps; the [guides](../guides/index.md) hold the steps. For the whole model on one
page first, read [chalkos in five minutes](../getting-started/five-minutes.md). The pages build on
each other in this order:

- [Architecture](architecture.md): the parts of chalkos, chalkd, chalkctl and chalklab, and how
  images and config become installed, running nodes.
- [Variants and platforms](variants.md): how mkImages builds a cluster's images from variants,
  platforms and NixOS modules, and how an extension adds a config kind.
- [Config and the inventory](config.md): the YAML documents that describe each node, how the
  inventory gives them out, and how chalkctl and chalkd check and apply them.
- [Change rules and facts](rules.md): the CEL rules with which an image decides which config
  changes a node accepts, and the node state they read.
- [Outputs and effects](outputs-and-effects.md): how a node renders and checks its services'
  config files before a delivery changes anything, and which deliveries restart or reload a
  service.
- [The image](image.md): the erofs store on dm-verity, the signed UKI and the A/B slots.
- [Boot, health and rollback](boot-and-rollback.md): how a node boots, decides whether a boot is
  healthy, and falls back from an image that is not.
- [Security model](security.md): what Secure Boot, the TPM, the OS CA and client roles protect, and
  what they do not.
- [Certificates](certificates.md): every certificate authority and certificate of a cluster, and
  how each is renewed and rotated.
- [Storage and encryption](storage.md): STATE, VAR and volumes, how they are encrypted and
  unlocked, and how they change after install.
- [Networking](networking.md): node addresses, dual stack, the VIP and packet filtering with
  nftables.
- [Kubernetes on chalkos](kubernetes.md): how chalkos runs etcd, the control plane, the kubelet and
  the cluster's add-ons.
- [Upgrades](upgrades.md): how a new image reaches a node, how chalkctl rolls it through a cluster,
  and what stops it.
