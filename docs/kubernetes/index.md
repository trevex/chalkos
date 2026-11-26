---
title: "Run Kubernetes"
description: "Plan and run Kubernetes clusters on chalkos nodes"
---

# Run Kubernetes

These pages are for operators who run a Kubernetes cluster on nodes whose image lists the Kubernetes
layer. They build on [Operate nodes](../operate/index.md): they assume a workstation with the
secrets file, and nodes installed as [Install a node](../operate/install.md) describes. Plan the
cluster before you install its nodes, because a few of its settings are fixed once it runs.

## Read in this order

1. [Plan a cluster](plan.md) fixes the settings that cannot change later.
2. [Bootstrap and get a kubeconfig](bootstrap.md) starts Kubernetes on the first control plane.
3. [Run a highly available control plane](ha-control-plane.md) adds the other control planes.
4. [Upgrade Kubernetes](upgrade-kubernetes.md) moves the cluster to a newer release.

## Set up

These pages take a cluster from its plan to a highly available control plane, which may serve the
cluster's images and whose nodes may announce their addresses over BGP, and run the workloads
that need kernel modules from the host.

<!-- docgen cards: plan.md bootstrap.md configure.md ha-control-plane.md cluster-registry.md bgp.md storage-and-gpu.md -->
<div class="grid cards" markdown>

-   __[Plan a cluster](plan.md)__

    ---

    Decide the control planes, the cluster endpoint, the address families and scheduling before installing a cluster

-   __[Bootstrap and get a kubeconfig](bootstrap.md)__

    ---

    Start the cluster on its first control plane, watch the other nodes join and issue kubeconfigs for the Kubernetes API

-   __[Configure Kubernetes](configure.md)__

    ---

    Set the cluster's settings, each node's labels, taints and flags, the pod network and your own add-ons

-   __[Run a highly available control plane](ha-control-plane.md)__

    ---

    Run three control planes behind a VIP, and add, replace or remove one while etcd keeps its quorum

-   __[Run an air-gapped cluster with the cluster registry](cluster-registry.md)__

    ---

    Serve container images from the control planes to a cluster that cannot reach their registries

-   __[Announce addresses with BGP](bgp.md)__

    ---

    Announce a Kubernetes node's addresses and the routed VIP with the layer's BGP speaker

-   __[Storage and GPU workloads](storage-and-gpu.md)__

    ---

    Run Rook and Ceph CSI, KubeVirt, Longhorn and NVIDIA's GPU operator on chalkos nodes

</div>
<!-- end docgen cards -->

## Maintain

These pages keep a running cluster current and fix what goes wrong.

<!-- docgen cards: upgrade-kubernetes.md rotate-kubernetes-cas.md troubleshooting.md -->
<div class="grid cards" markdown>

-   __[Upgrade Kubernetes](upgrade-kubernetes.md)__

    ---

    Move a cluster to a new Kubernetes release: the control plane through config first, then the kubelets through new images

-   __[Rotate the Kubernetes CAs and keys](rotate-kubernetes-cas.md)__

    ---

    Replace the Kubernetes CAs, the service-account key and the encryption key phase by phase, and revoke Kubernetes access

-   __[Troubleshoot Kubernetes](troubleshooting.md)__

    ---

    Find out why a node's Kubernetes does not start, join, serve logs, reach other pods or get its kubelet certificates

</div>
<!-- end docgen cards -->
