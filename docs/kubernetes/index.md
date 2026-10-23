---
title: "Run Kubernetes"
description: "Plan and run Kubernetes clusters on chalkos nodes"
---

# Run Kubernetes

These pages are for operators who run a Kubernetes cluster on chalkos nodes. They add what a
cluster needs on top of [Operate nodes](../operate/index.md): planning it, bootstrapping it, running
its control plane on several nodes, upgrading Kubernetes, rotating its CAs and keys, and finding
out why it misbehaves.

<div class="grid cards" markdown>

-   :material-clipboard-text-outline: __Plan a production cluster__

    ---

    Decide the variants, addresses, storage, keys and time sources of a production cluster before
    installing it.

    [:octicons-arrow-right-24: Read](production-cluster.md)

-   :material-play-circle-outline: __Bootstrap a cluster__

    ---

    Start the cluster on its first control plane, and issue kubeconfigs for its Kubernetes API.

    [:octicons-arrow-right-24: Read](bootstrap.md)

-   :material-sitemap: __Run a highly available control plane__

    ---

    Run three control planes behind a virtual IP, and add, replace or remove one.

    [:octicons-arrow-right-24: Read](ha-control-plane.md)

-   :material-update: __Upgrade Kubernetes__

    ---

    Move to a new Kubernetes release, and keep etcd's quorum and the workloads safe while nodes
    reboot.

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
