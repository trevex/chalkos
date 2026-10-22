---
title: "chalkos"
description: "An image-based NixOS for Kubernetes nodes"
---

# chalkos

chalkos turns bare-metal machines and virtual machines into a Kubernetes cluster that runs an
immutable operating system managed through an API. Nix builds the cluster's disk
[images](reference/glossary.md#image), one per [variant](reference/glossary.md#variant) and
platform, each holding an erofs Nix store on [dm-verity](reference/glossary.md#dm-verity) and a
[unified kernel image](reference/glossary.md#uki) that [Secure
Boot](reference/glossary.md#secure-boot) verifies. Each node's settings are YAML [config
documents](reference/glossary.md#config-document), which an
[inventory](reference/glossary.md#inventory) assigns to it. Nodes keep their state on encrypted
partitions sealed to the [TPM](reference/glossary.md#tpm), upgrade between
[A/B](reference/glossary.md#a-b) slots and fall back to the image before when a new one does not
boot healthy. [chalkd](reference/glossary.md#chalkd) on each node serves the node API,
[chalkbuild](reference/glossary.md#chalkbuild) signs the images Nix builds,
[chalkctl](reference/glossary.md#chalkctl) installs, configures and operates the nodes without
running Nix, and [chalklab](reference/glossary.md#chalklab) runs a whole cluster as QEMU virtual
machines on one machine.

## Who is chalkos for?

chalkos is for people who run Kubernetes on their own machines, bare metal or virtual machines
under KVM, and want the nodes to be appliances: every node of a variant boots the same verified
image, nobody logs in to change it, and an upgrade either becomes healthy or rolls back by
itself. It suits teams that already describe systems in Nix, because a chalkos image is built from
NixOS modules and a variant can use any NixOS module. Only the images need Nix: chalkctl reads the
YAML files and the image directories a build produced, so the machine that installs and operates
the nodes needs no Nix.

It is a poor fit where a node needs hand changes or interactive logins, and today where the
machines are neither x86-64 bare metal nor KVM virtual machines. Nodes have no SSH and no shell;
debugging goes through the node API and `kubectl debug node/<node>`.

## How does chalkos compare with Talos?

chalkos follows the operating model of [Talos Linux](https://www.talos.dev/): an immutable OS
without logins, managed through a mutual-TLS API with a [client
file](reference/glossary.md#client-file) of a [client role](reference/glossary.md#client-role)
(reader, operator or admin), A/B upgrades and a control plane that holds its virtual IP through an
etcd election. It differs where Nix changes the trade-offs:

| Topic | Talos | chalkos |
| --- | --- | --- |
| Configuration | One machine configuration per node in Talos's own YAML schema, applied through the API. | YAML documents in the style of Kubernetes objects, one kind per concern, such as `Node`, `Network`, `SystemDisk` and `Cluster`, which an inventory assigns to nodes. chalkctl and chalkd check them against the JSON Schemas the image carries, and a NixOS module in the image can declare a kind of its own. |
| Images | One image for every node, extended with signed system extensions. | One image per variant and [platform](reference/glossary.md#platform), built from NixOS modules, so a GPU variant carries the GPU drivers and the others do not. The kernel is nixpkgs's, with its modules filtered per image, so the binary cache keeps working. |
| Node certificates | Control planes hold the root CA's key. | Control planes hold a [node CA](reference/glossary.md#node-ca), an intermediate that may issue only server and client certificates. The [OS CA](reference/glossary.md#os-ca)'s key stays in the [secrets file](reference/glossary.md#secrets-file). |
| Upgrades | The node pulls an installer image from a registry. | chalkctl streams an image Nix built to chalkd, which writes it into the inactive slot. |
| Maturity | Released, with images for many clouds, hypervisors and boards. | Unreleased; bare metal and KVM on x86-64. |

## How does chalkos compare with plain NixOS?

A NixOS machine changes in place: `nixos-rebuild` builds a new generation into a writable Nix
store on the machine and switches to it. A chalkos node has neither a Nix daemon nor
`switch-to-configuration`. Its store is a read-only erofs file system checked block by block by
dm-verity, its root is a tmpfs and its `/etc` is read-only, so the running system is exactly the
image that was built and signed. A change to its software becomes a new image, which chalkd
writes into the inactive [slot](reference/glossary.md#slot) and boots with
[boot counting](reference/glossary.md#boot-counting); a boot that does not become healthy returns
to the image before without anyone at the console.

What stays from NixOS is the module system. A variant's modules are ordinary NixOS modules, so
services, kernel settings and packages are declared as on any NixOS machine. The cost is that
every change to a node's software is an image upgrade with a reboot; only its
[config](reference/glossary.md#config) (hostname, network, labels, taints, storage, time servers,
the cluster's Kubernetes settings and extension documents) changes without one.

## What is the status of chalkos?

chalkos is unreleased. Until a first release, the config kinds, all at `v1alpha1`, the arguments
of `mkImages`, the image options, the commands and the node API change without migrations, so a
cluster may need its YAML or its flake edited, or its nodes reinstalled, when it moves to a newer
chalkos. Run it in labs and test clusters.

## Where to start

Each section of the documentation serves one kind of reader:

- New to chalkos: the [quick start](getting-started/index.md) runs a cluster of two virtual
  machines on one machine with chalklab, upgrades it and removes it, in about 15 minutes.
  [chalkos in five minutes](getting-started/five-minutes.md) then gives the model of a cluster on
  one page, and [Requirements](getting-started/requirements.md) lists what real machines need.
- Running a cluster: the [guides](guides/index.md) install, upgrade, rotate and recover real
  clusters, one task per page.
- Weighing the design: the [concepts](concepts/index.md) explain how chalkos works, why, and
  where its protections stop.
- Looking something up: the [reference](reference/index.md) lists the commands, the config kinds,
  the arguments of `mkImages` and the image options, the node API, the disk layout, the ports and
  the [glossary](reference/glossary.md).
- Changing chalkos: [contributing](contributing/index.md) covers the development setup, the tests,
  the code's layout and how these pages are written.
