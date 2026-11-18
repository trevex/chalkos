---
title: "Run Kubernetes"
description: "Plan and run Kubernetes clusters on chalkos nodes"
---

# Run Kubernetes

These pages are for operators who run a Kubernetes cluster on nodes whose image lists the Kubernetes
layer. They build on [Operate nodes](../operate/index.md): they assume a workstation with the
secrets file, and nodes installed as [Install a node](../operate/install.md) describes. Plan the
cluster before you install its nodes, because a few of its settings are fixed once it runs. The
other pages follow in order: bootstrap the cluster, configure it, run its control plane on several
nodes, serve its images from the control planes, announce its addresses over BGP, upgrade
Kubernetes, rotate its CAs and keys, and find out why it misbehaves.

## Set up

These pages take a cluster from its plan to a highly available control plane, which may serve the
cluster's images and whose nodes may announce their addresses over BGP.

<div class="grid cards" markdown>

-   :material-clipboard-text-outline: __Plan a cluster__

    ---

    Decide the control planes, the cluster endpoint, the address families and scheduling before
    installing a cluster.

    [:octicons-arrow-right-24: Read](plan.md)

-   :material-play-circle-outline: __Bootstrap and get a kubeconfig__

    ---

    Start the cluster on its first control plane, watch the other nodes join and issue
    kubeconfigs for its Kubernetes API.

    [:octicons-arrow-right-24: Read](bootstrap.md)

-   :material-cog-outline: __Configure Kubernetes__

    ---

    Set the cluster's settings, each node's labels, taints and flags, the pod network and your
    own add-ons.

    [:octicons-arrow-right-24: Read](configure.md)

-   :material-sitemap: __Run a highly available control plane__

    ---

    Run three control planes behind a virtual IP, and add, replace or remove one.

    [:octicons-arrow-right-24: Read](ha-control-plane.md)

-   :material-database-arrow-down: __Run an air-gapped cluster with the cluster registry__

    ---

    Serve container images from the control planes to a cluster that cannot reach their
    registries, and push, check and delete them on every control plane.

    [:octicons-arrow-right-24: Read](cluster-registry.md)

-   :material-transit-connection-variant: __Announce addresses with BGP__

    ---

    Announce the nodes' addresses and the routed VIP to the fabric's routers, and take the
    default route they send.

    [:octicons-arrow-right-24: Read](bgp.md)

</div>

## Maintain

These pages keep a running cluster current and fix what goes wrong.

<div class="grid cards" markdown>

-   :material-update: __Upgrade Kubernetes__

    ---

    Move to a new Kubernetes release, and keep etcd's quorum and the workloads within their
    PodDisruptionBudgets while nodes reboot.

    [:octicons-arrow-right-24: Read](upgrade-kubernetes.md)

-   :material-key-change: __Rotate the Kubernetes CAs and keys__

    ---

    Replace the Kubernetes CAs, the service-account key and the encryption key, and revoke
    Kubernetes access.

    [:octicons-arrow-right-24: Read](rotate-kubernetes-cas.md)

-   :material-stethoscope: __Troubleshoot Kubernetes__

    ---

    Find out why a node's Kubernetes does not start, join, serve logs or reach other nodes'
    pods.

    [:octicons-arrow-right-24: Read](troubleshooting.md)

</div>
