---
title: "Requirements"
description: "What chalkos needs from machines, firmware, disks and networks, and what a lab needs from its host"
---

# Requirements

This page lists what chalkos needs from a node, from the network around it, from the machine an
operator runs chalkctl on and from a host that runs a [lab](../reference/glossary.md#lab). The
sizes and defaults come from the `chalkos.*` options; the memory figures were measured in the
end-to-end tests.

## Nodes

A node is the machine that runs a role [image](../reference/glossary.md#image), on bare metal or
as a virtual machine under KVM.

| Requirement | What chalkos needs | Without it |
| --- | --- | --- |
| CPU architecture | x86-64. Images are built for [`chalkos.cluster.system`](../reference/options.md#chalkosclustersystem), `x86_64-linux` by default. | chalkd and the partition layout know arm64, but no arm64 image has been built or tested. |
| Firmware | UEFI. systemd-boot and the [UKI](../reference/glossary.md#uki) are EFI binaries. | A BIOS-only machine does not boot an image. |
| Secure Boot | On, with the certificate that signs the images in the firmware's [db](../reference/glossary.md#db-and-dbx). chalkos does not enroll keys; the operator does, as [Sign images for Secure Boot](../guides/secure-boot-signing.md) shows. | The image boots, but nothing verifies the UKI, so nothing verifies the store either. [PCR 7](../reference/glossary.md#pcr-7) then records Secure Boot as off, a state that any boot chain with Secure Boot off reproduces, so the TPM unseals STATE and VAR for whatever boots on the machine. chalkd checks the signatures of an upgrade's UKI and boot loader against db and dbx only while Secure Boot is enforced. |
| TPM | TPM 2.0, for the default encryption mode `tpm2`, which seals the keys of [STATE](../reference/glossary.md#state), [VAR](../reference/glossary.md#var) and encrypted volumes to PCR 7. | Install fails with mode `tpm2`. Set [`chalkos.roles.<name>.storage.encryption.mode`](../reference/options.md#chalkosrolesstorageencryptionmode) to `none`, which leaves STATE and the node's secrets unencrypted on the disk. |

A node's memory follows mostly from its workloads. Measured before any workload, a single
control plane uses about 0.7 GiB after its bootstrap and 1.1 GiB after a reboot, each control
plane of three about 860 MiB, and a worker about 0.3 GiB. The end-to-end tests run control
planes in 1.5 to 2 GiB and a worker in 1 GiB.

The system disk holds a fixed system region and VAR, which takes the rest of the disk by default.
The sizes are options of a role's NixOS modules.

| Partition | Default size | Option |
| --- | --- | --- |
| [ESP](../reference/glossary.md#esp) | 1 GiB | [`chalkos.disk.espSize`](../reference/options.md#chalkosdiskespsize) |
| Store, slot A and slot B | 3 GiB each | [`chalkos.disk.storeSize`](../reference/options.md#chalkosdiskstoresize) |
| Store hash tree, slot A and slot B | 128 MiB each | [`chalkos.disk.storeVeritySize`](../reference/options.md#chalkosdiskstoreveritysize) |
| STATE | 128 MiB | [`chalkos.disk.stateSize`](../reference/options.md#chalkosdiskstatesize) |
| VAR | the rest of the disk | [`chalkos.nodes.<name>.storage.var.size`](../reference/options.md#chalkosnodesstoragevarsize) |

The system region takes about 7.4 GiB. VAR holds etcd, containerd's images and the logs, so its
size follows from the workloads; a lab's 16 GiB disk leaves about 8.6 GiB for it, enough to try
chalkos. [Storage and encryption](../concepts/storage.md) explains the layout and its limits.

## Network

| Requirement | What chalkos needs |
| --- | --- |
| Addresses | Static addresses declared in the node's systemd-networkd configuration, [`chalkos.nodes.<name>.network`](../reference/options.md#chalkosnodesnetwork), or DHCP on interfaces that no network of the node matches. A control plane that joined etcd keeps the addresses it joined with. |
| Ports | chalkd listens on TCP 50000 and a control plane's API server on TCP 6443; [Ports and firewall](../reference/ports-and-firewall.md) lists every port between nodes and from operators. |
| Time | Reachable time servers, because certificates are checked against the clock and a node whose clock is off reaches no other node. The default servers are `ptbtime1.ptb.de` to `ptbtime3.ptb.de`, authenticated with NTS; [`chalkos.time.servers`](../reference/options.md#chalkostimeservers) replaces them. |
| Container registries | Access to `registry.k8s.io` for the control plane, etcd, CoreDNS and pause images and to `ghcr.io` for flannel, or mirrors of them in [`chalkos.cluster.registries.mirrors`](../reference/options.md#chalkosclusterregistriesmirrors). The kubelet and containerd are in the image. |

## Operator's machine

The operator's machine builds the images and runs chalkctl and kubectl.

| Requirement | What chalkos needs |
| --- | --- |
| Nix | Nix with the `nix-command` and `flakes` features. The images are `x86_64-linux` builds, so the machine is an x86-64 Linux machine or has a Nix builder for one. |
| git | The cluster's flake lives in a git repository, and a flake sees only the files git tracks. |
| chalkctl and kubectl | From the flake's development shell, as the `lab` template defines it, or from the chalkos flake's packages. |
| age identity | To decrypt `secrets.age`: an age key in `~/.config/chalkos/age.key`, or the SSH key `~/.ssh/id_ed25519` or `~/.ssh/id_rsa` the file was encrypted to, or a file named with `--identity`. A [client file](../reference/glossary.md#client-file) needs none. |
| Network | Reach to each node's TCP 50000 and to the API server's TCP 6443. |

## Lab host

[chalklab](../reference/glossary.md#chalklab) runs a cluster's `kvm` nodes as QEMU virtual
machines on one machine, without root.

| Requirement | What chalklab needs |
| --- | --- |
| CPU and KVM | An x86-64 Linux machine with virtualisation enabled in its firmware, and read and write access to `/dev/kvm`, which most distributions grant to the `kvm` group. `chalklab create` stops at once without it. |
| Memory | 3 GiB per control plane and 2 GiB per other node by default, so about 5 GiB of free memory for the `lab` template's two nodes. `--controlplane-memory` and `--memory` change it. |
| Disk | A sparse 16 GiB disk per node in `$XDG_STATE_HOME/chalklab/<cluster>`, which grows as the node writes, plus a signed copy of each role's image. The `lab` template's lab takes about 2.1 GiB after `chalklab create`. |
| State directory | A path short enough for the lab's Unix sockets, which may be at most 107 bytes long. With a deep `$XDG_STATE_HOME`, `chalklab create` refuses and asks for a shorter one. |
| Nix | Nix with flakes; the chalklab package brings QEMU, OVMF with Secure Boot, swtpm and the lab network's switch. |

## What is untested

chalkos is tested on x86-64 only: on QEMU with KVM in the end-to-end tests and the lab, and with
the `metal` platform's images in QEMU. arm64, VMware ESXi, Hyper-V and the cloud platforms have
no platform definition and no tests; the `virtualisation` module group carries their drivers, so
a custom platform can start from it.
