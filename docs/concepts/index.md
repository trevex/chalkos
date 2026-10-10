---
title: "Concepts"
description: "How chalkos works and why it works that way"
---

# Concepts

These pages explain how chalkos works and why it is built the way it is: how a cluster definition
becomes signed images, how a node boots, unlocks and falls back from a bad image, and how it runs
Kubernetes. They describe mechanisms and their limits rather than steps; the
[guides](../guides/index.md) hold the steps. For the whole model on one page first, read
[chalkos in five minutes](../getting-started/five-minutes.md). The pages build on each other in
this order:

- [Architecture](architecture.md): the parts of chalkos, chalkd, chalkctl and chalklab, and how a
  cluster definition becomes installed, running nodes.
- [The cluster definition](cluster-definition.md): flakes, roles, nodes and platforms, and how
  chalkos evaluates them into images and identities.
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
