---
title: "Boot and the image"
description: "The units that run at every boot of a node, and what an image fixes versus what its config delivers"
---

# Boot and the image

This page is for operators and image authors. It lists the units that do chalkos's work at
every boot of a node, for reading a node's logs. It also lists what an image fixes when it is
built and what reaches a node in its config, for deciding whether a change needs a new image or a
config delivery.

## Boot units

These units do chalkos's work when an installed node boots. The second column says when each runs.

| Unit | Runs | Command | What it does |
| --- | --- | --- | --- |
| `chalkos-state.service` | in the initrd | `chalkos-storage state` | Unlocks and mounts STATE, with the TPM or with the second key the console asks for |
| `chalkos-storage.service` | in the initrd | `chalkos-storage initrd` | Creates the volumes recorded on STATE and mounts VAR |
| `chalkos-config.service` | before networkd | `chalkd load-config` | Applies the config on STATE: the hostname, the documents in `/run/chalkos/config`, the secrets the granted units read, and the outputs, networkd's units and chrony's time servers among them |
| `chalkd.service` | after the network | `chalkd serve` | Serves the node API and carries out its requests |
| `chalkos-kubernetes.service` | on images with Kubernetes, once the network is online | `chalkd prepare-kubernetes` | Waits for the node's addresses and prepares its Kubernetes certificates, kubeconfigs and kubelet flags |
| `chalkos-health.service` | only on a boot that systemd-boot counts | `chalkd health` | Decides whether a new image is healthy |

The installer runs none of `chalkos-state.service`, `chalkos-storage.service` and
`chalkos-config.service`.

## What an image fixes and what config delivers

The left column changes only with a new image and an upgrade. The right column changes with a
config delivery, without a reboot.

| Fixed when the image is built | Delivered at runtime, in the config |
| --- | --- |
| Kernel, kernel modules and initrd | Hostname, in `Host`, and whether the node is a control plane or a worker, in `Node` |
| NixOS services and the variant's modules, the services of extensions among them | systemd-networkd units, in `Network`, `NetDev` and `Link` |
| Kubernetes release, the images of its components and the CNI provider | The cluster's endpoint and its Kubernetes settings: ranges, VIP, extra flags and addons, in `Cluster`, `Flannel` and `Addon`, and registry mirrors in `Mirror` |
| The OS CA for maintenance mode, from the `osCA` argument of mkImages | The kubelet's addresses, labels and taints, in `Node` too |
| Partition sizes of the system region | Storage: system disk, VAR, volumes and encryption, in `SystemDisk` and `Volume` |
| Boot tries and health timeout | Time servers, in `TimeSync` |
| The config kinds the image accepts and their schemas | Documents of the kinds extensions declare, and the `Secret`s they reference |

Sources: `modules/node/base/storage.nix`, `modules/node/chalkd.nix`, `modules/node/kubernetes.nix`,
`modules/node/upgrade.nix`, `modules/installer/default.nix`, `lib/images-arguments.nix` and
`modules/node/kinds`.
