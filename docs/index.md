---
title: "chalkos"
description: "An image-based NixOS for Kubernetes nodes"
---

# chalkos

chalkos turns bare-metal machines and virtual machines into a Kubernetes cluster that runs an
immutable operating system managed through an API. A cluster is declared in a Nix flake. Each
[role](reference/glossary.md#role) of the cluster becomes a disk [image](reference/glossary.md#image)
holding an erofs Nix store on [dm-verity](reference/glossary.md#dm-verity) and a
[unified kernel image](reference/glossary.md#uki) that [Secure Boot](reference/glossary.md#secure-boot)
verifies. Nodes keep their state on encrypted partitions sealed to the
[TPM](reference/glossary.md#tpm), upgrade between [A/B](reference/glossary.md#a-b) slots and fall
back to the image before when a new one does not boot healthy. [chalkd](reference/glossary.md#chalkd)
on each node serves the node API, [chalkctl](reference/glossary.md#chalkctl) installs and operates
the nodes, and [chalklab](reference/glossary.md#chalklab) runs a whole cluster as QEMU virtual
machines on one machine.

## Who is chalkos for?

chalkos is for people who run Kubernetes on their own machines, bare metal or virtual machines
under KVM, and want the nodes to be appliances: every node of a role boots the same verified
image, nobody logs in to change it, and an upgrade either becomes healthy or rolls back by
itself. It suits teams that already describe systems in Nix, because a chalkos cluster is
declared in Nix modules and a role can use any NixOS module.

It is a poor fit where a node needs hand changes or interactive logins, and today where the
machines are neither x86-64 bare metal nor KVM virtual machines. Nodes have no SSH and no shell;
debugging goes through the node API and `kubectl debug node/<node>`.

## How does chalkos compare with Talos?

chalkos follows the operating model of [Talos Linux](https://www.talos.dev/): an immutable OS
without logins, managed through a mutual-TLS API with a client file of a role (reader, operator
or admin), A/B upgrades and a control plane that holds its virtual IP through an etcd election.
It differs where Nix changes the trade-offs:

| Topic | Talos | chalkos |
| --- | --- | --- |
| Configuration | A machine configuration in Talos's own YAML schema, applied through the API. | Nix modules, evaluated when the image is built. chalkd and chalkctl have no configuration schema of their own; a node receives only a small [identity](reference/glossary.md#identity) at runtime. |
| Images | One image for every node, extended with signed system extensions. | One image per role and [platform](reference/glossary.md#platform), so a GPU role carries the GPU drivers and the others do not. The kernel is nixpkgs's, with its modules filtered per role, so the binary cache keeps working. |
| Node certificates | Control planes hold the root CA's key. | Control planes hold a [node CA](reference/glossary.md#node-ca), an intermediate that may issue only server and client certificates. The [OS CA](reference/glossary.md#os-ca)'s key stays in the [secrets file](reference/glossary.md#secrets-file). |
| Upgrades | The node pulls an installer image from a registry. | chalkctl builds the image and streams it to chalkd, which writes it into the inactive slot. |
| Maturity | Released, with images for many clouds, hypervisors and boards. | Unreleased; bare metal and KVM on x86-64. |

## How does chalkos compare with plain NixOS?

A NixOS machine changes in place: `nixos-rebuild` builds a new generation into a writable Nix
store on the machine and switches to it. A chalkos node has neither a Nix daemon nor
`switch-to-configuration`. Its store is a read-only erofs file system checked block by block by
dm-verity, its root is a tmpfs and its `/etc` is read-only, so the running system is exactly the
image that was built and signed. A change becomes a new image, which chalkd writes into the
inactive [slot](reference/glossary.md#slot) and boots with
[boot counting](reference/glossary.md#boot-counting); a boot that does not become healthy returns
to the image before without anyone at the console.

What stays from NixOS is the module system. A role's `nixosModules` are ordinary NixOS modules,
so services, kernel settings and packages are declared as on any NixOS machine. The cost is
that every change to a node's software is an image upgrade with a reboot; only the identity
(hostname, network, labels, taints, storage, time servers) changes without one.

## What is the status of chalkos?

chalkos is unreleased. The [manifest](reference/glossary.md#manifest) that chalkctl reads has
`schemaVersion` 0, which marks its format as unstable. Until a first release, the `chalkos.*`
options, the commands and the node API change too, without migrations, so a cluster may need its
definition edited or its nodes reinstalled when it moves to a newer chalkos. Run it in labs and
test clusters.

## Where to start

- [Quick start](getting-started/index.md): a cluster of two virtual machines on this machine with
  chalklab, an upgrade and the teardown, in about 15 minutes.
- [chalkos in five minutes](getting-started/five-minutes.md): the model of a cluster on one page.
- [Concepts](concepts/index.md): how chalkos works and why.
- [Guides](guides/index.md): installing, upgrading and operating real clusters.
- [Reference](reference/index.md): commands, options, the node API and the glossary.
- [Contributing](contributing/index.md): developing chalkos and its documentation.
