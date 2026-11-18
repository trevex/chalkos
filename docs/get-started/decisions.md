---
title: "Decisions before you install"
description: "The choices that shape a chalkos cluster before its first install, each with its consequence and the page that carries it out"
---

# Decisions before you install

A few choices are hard to change once nodes run. Make them before the first install of a cluster
on your own machines. The [quick start](quick-start-kubernetes.md) makes all of them for you, so you can
skip this page for a lab.

| Decision | Default choice | Changing it later |
| --- | --- | --- |
| Secure Boot or not | Secure Boot | Turning it on for an installed node changes PCR 7, so the node's next boot asks for its recovery key. |
| One or three control planes | Three | A cluster can grow from one to three; the endpoint should allow for it. |
| VIP announced on layer 2 or routed | Layer 2 | The mode changes with a config delivery; moving the endpoint means reissuing every kubeconfig. |
| Secrets file encrypted with age or plaintext | age | A rotation can write a plaintext file encrypted to a new `.age` file. |
| Metal or VM | The machines you have | A node's platform changes only with a reinstall. |

## Secure Boot or not

With Secure Boot enforced, the firmware starts only images signed with your db key. An operator
can then roll out only what the key holder signed. Without it, anyone with an operator certificate
can install an image of their own and run it as root, with STATE and VAR unlocked, because their
keys are sealed to PCR 7 alone.

Secure Boot costs a key directory to keep offline, a signing step after each build, and an
enrolment of your keys in each machine's firmware.
[Secure Boot limits a node to images the key holder signed](../how-it-works/node/security.md#secure-boot-limits-a-node-to-images-the-key-holder-signed)
explains the mechanism, and [Sign images for Secure Boot](../build/sign-images.md) creates the
keys and signs the images.

## One or three control planes

With the Kubernetes layer, every control plane is an etcd member, and etcd needs a majority to
accept a write. One control plane survives no machine's loss, and its API server is down while it
reboots, so each upgrade needs `--allow-downtime`. Three survive the loss of one and upgrade one
at a time. [Plan a cluster](../kubernetes/plan.md#choose-one-or-three-control-planes) compares
the counts, and [Run a highly available control plane](../kubernetes/ha-control-plane.md)
installs three.

## A VIP on layer 2 or routed

The cluster endpoint must outlive any one control plane. A VIP that a healthy control plane holds
does that. In the default mode, `l2`, the holder announces the VIP with ARP and neighbour
advertisements, so the control planes share one layer-2 segment. In `routed` mode, a routing
daemon on each control plane announces it, and the control planes may sit on different links. A
load balancer you run in front of the control planes replaces the VIP.
[Choose the cluster endpoint](../kubernetes/plan.md#choose-the-cluster-endpoint) explains each.

Every kubeconfig names the endpoint, so moving it later means issuing every kubeconfig again. A
VIP from the start, even on one control plane, keeps the endpoint when control planes join.

## age or plaintext secrets

The secrets file holds every key of the cluster. Encrypted with age to each administrator's key
and an offline backup key, it can live in the repository. A plaintext file must be kept out of
version control and protected by other means; `chalkctl gen secrets --plaintext` warns about it.
[Generate the cluster's secrets with age](../operate/secrets.md#generate-the-clusters-secrets-with-age)
writes an encrypted one.

## Metal or VM

An image is built for one platform. `metal` suits physical machines and shows the console on the
serial port and the screen. `kvm` suits virtual machines under KVM, with virtio drivers and a QEMU
guest agent. A node refuses an image of another platform than the one it runs.
[Install a node](../operate/install.md) installs a physical machine, and
[Install a node in a VM](../operate/install-vm.md) a virtual one.

## What next

[Requirements](requirements.md) lists what the nodes, the workstation and the network need. Then
[Set up your workstation](../operate/workstation.md).
