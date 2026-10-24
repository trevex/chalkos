---
title: "Requirements"
description: "What chalkos needs from machines, firmware, disks and networks, and from the workstation, build host and lab host"
---

# Requirements

This page lists what chalkos needs from a node and the network around it, and from the machines
an operator builds images, runs chalkctl and runs a lab on. The sizes and defaults come from the
image options and the [config kinds](../reference/kinds.md); the memory figures were measured in
the end-to-end tests.

## Nodes and network

A node is the machine that runs a chalkos image, on bare metal or as a virtual machine under KVM.

### Machine and firmware

| Requirement | What chalkos needs | Without it |
| --- | --- | --- |
| CPU architecture | x86-64. Images are built for the image option `system`, `x86_64-linux` by default. | chalkd and the partition layout know arm64, but no arm64 image has been built or tested. |
| Firmware | UEFI. systemd-boot and the UKI are EFI binaries. | A BIOS-only machine does not boot an image. |
| Secure Boot | On, with the certificate that signs the images in the firmware's db. | The image boots, but nothing verifies the UKI or the store, and the TPM unseals STATE and VAR for whatever boots. |
| TPM | TPM 2.0, for the default encryption mode `tpm2`, which seals the keys of STATE, VAR and encrypted volumes to PCR 7. | Install fails with mode `tpm2`. Set `spec.encryption.mode` of the node's `SystemDisk` document to `none`, which leaves STATE and the node's secrets unencrypted on the disk. |

The image option [`system`](../reference/mkimages.md#system) sets the architecture, and the
[`SystemDisk`](../reference/kinds.md#systemdisk) kind describes the encryption modes.

chalkos does not enrol Secure Boot keys on a real machine. The operator enrols the certificate
that signs the images, as [Sign images for Secure Boot](../build/sign-images.md) shows; chalklab
enrols its own keys in the lab's virtual machines.

With Secure Boot off, PCR 7 records Secure Boot as off. Any boot chain with Secure Boot off
reproduces that state, so the TPM unseals STATE and VAR for whatever boots on the machine. chalkd
also checks the signatures of an upgrade's UKI and boot loader against db and dbx only while
Secure Boot is enforced.

### Memory

A node's memory follows mostly from its workloads. Measured before any workload, a single control
plane uses about 0.7 GiB after its bootstrap and 1.1 GiB after a reboot, each control plane of
three about 860 MiB, and a worker about 0.3 GiB. The end-to-end tests run control planes in 1.5 to
2 GiB and a worker in 1 GiB.

### System disk

The system disk holds a fixed system region and VAR, which takes the rest of the disk by default.
The sizes of the system region are options of a variant's NixOS modules; VAR's is the node's config.

| Partition | Default size | Option |
| --- | --- | --- |
| ESP | 1 GiB | `chalkos.disk.espSize` |
| Store, slot A and slot B | 3 GiB each | `chalkos.disk.storeSize` |
| Store hash tree, slot A and slot B | 128 MiB each | `chalkos.disk.storeVeritySize` |
| STATE | 128 MiB | `chalkos.disk.stateSize` |
| VAR | the rest of the disk | `spec.var.size` of the node's `SystemDisk` document |

The system region takes about 7.4 GiB. VAR holds etcd, containerd's images and the logs, so its
size follows from the workloads; a lab's 16 GiB disk leaves about 8.6 GiB for it, enough to try
chalkos. The [image options](../reference/image-options.md#chalkosdiskespsize) describe the
`chalkos.disk` options, and [Storage and encryption](../how-it-works/node/storage.md) explains the
layout and its limits.

### Network

| Requirement | What chalkos needs |
| --- | --- |
| Addresses | Static addresses in the node's `Network` documents, or DHCP on interfaces that none of them matches. A control plane that joined etcd keeps the addresses it joined with. |
| Ports | chalkd listens on TCP 50000 and a control plane's API server on TCP 6443. |
| Time | Reachable time servers, because certificates are checked against the clock and a node whose clock is off reaches no other node. The default servers are `ptbtime1.ptb.de` to `ptbtime3.ptb.de`, authenticated with NTS; `spec.servers` of a `TimeSync` document replaces them. |
| Container registries | Access to `registry.k8s.io` for the control plane, etcd, kube-proxy, CoreDNS and pause images and to `ghcr.io` for flannel, or mirrors of them in `Mirror` documents. The kubelet and containerd are in the image. |

[Ports and firewall](../reference/ports-and-firewall.md) lists every port between nodes and from
operators. The kinds [`Network`](../reference/kinds.md#network),
[`TimeSync`](../reference/kinds.md#timesync) and [`Mirror`](../reference/kinds.md#mirror) describe
the documents that set addresses, time servers and mirrors.

## Workstation and build host

Building images and operating nodes are separate, and may happen on separate machines. The build
host runs Nix and builds the image directories. The workstation runs chalkctl, which never runs
Nix: it reads the image directories with the inventory and its YAML files.

### Build host

| Requirement | What chalkos needs |
| --- | --- |
| Nix | Nix with the `nix-command` and `flakes` features. The images are `x86_64-linux` builds, so the machine is an x86-64 Linux machine or has a Nix builder for one. |
| git | The flake that calls `mkImages` lives in a git repository, and a flake sees only the files git tracks. |

### Workstation

| Requirement | What chalkos needs |
| --- | --- |
| chalkctl and kubectl | From the chalkos flake's packages or the `lab` template's development shell. |
| mtools | `mdir` and `mcopy`, which `install` and `upgrade` run to read the ESP of an image directory. The flake's chalkctl brings them. |
| Files | The inventory, `chalkos.yaml`, the YAML files it names, and the image directories the inventory names for the nodes, for installs, upgrades and `chalkctl validate`. |
| age identity | To decrypt `secrets.age`: an age key in `~/.config/chalkos/age.key`, or the SSH key `~/.ssh/id_ed25519` or `~/.ssh/id_rsa` the file was encrypted to, or a file named with `--identity`. A client file needs none. |
| Network | Reach to each node's TCP 50000 and to the API server's TCP 6443. |

The flake's `bin/chalkctl` is a wrapper script that puts mtools on the `PATH` and runs
`bin/.chalkctl-wrapped`. That program is built without cgo, so it is a static binary. To run
chalkctl on a machine without Nix, an airgapped one included, copy `bin/.chalkctl-wrapped` there
as `chalkctl` and install the distribution's mtools package.
[Set up your workstation](../operate/workstation.md#chalkctl-comes-from-the-chalkos-flake) shows how
to take chalkctl from the flake.

## Lab host

chalklab runs a cluster's `kvm` nodes as QEMU virtual machines on one machine, without root.

| Requirement | What chalklab needs |
| --- | --- |
| CPU and KVM | An x86-64 Linux machine with virtualisation enabled in its firmware, and read and write access to `/dev/kvm`, which most distributions grant to the `kvm` group. `chalklab create` stops at once without it. |
| Memory | 3 GiB per control plane and 2 GiB per other node by default, so about 5 GiB of free memory for the `lab` template's two nodes. The lab file's `memory` changes it per node. |
| Disk | A sparse 16 GiB disk per node in `$XDG_STATE_HOME/chalklab/<name>`, which grows as the node writes, plus a signed copy of the image for each node. The `lab` template's lab takes about 2.1 GiB after `chalklab create`. |
| State directory | A path short enough for the lab's Unix sockets, which may be at most 107 bytes long. With a deep `$XDG_STATE_HOME`, `chalklab create` refuses and asks for a shorter one. |
| Nix | Nix with flakes; the chalklab package brings QEMU, OVMF with Secure Boot, swtpm and the lab network's switch. |

## What is untested

chalkos is tested on x86-64 only: on QEMU with KVM in the end-to-end tests and the lab, and with
the `metal` platform's images in QEMU. arm64, VMware ESXi, Hyper-V and the cloud platforms have no
platform definition and no tests. The `virtualisation` module group carries their drivers, so a
custom platform can start from it.
