---
title: "Operate nodes"
description: "Install, upgrade, recover and troubleshoot chalkos nodes"
---

# Operate nodes

These pages are for operators who run nodes with chalkctl and YAML files. They cover setting up
a workstation, writing nodes' config, installing nodes, adding storage, upgrading, pulling images
from registries, rotating certificates, recovering a node and troubleshooting.
Planning and setting up a Kubernetes cluster is in [Run Kubernetes](../kubernetes/index.md).

<div class="grid cards" markdown>

-   :material-laptop: __Set up your workstation__

    ---

    Get chalkctl, generate the cluster's secrets and choose the credentials each command needs.

    [:octicons-arrow-right-24: Read](workstation.md)

-   :material-file-cog: __Write a node's config__

    ---

    Give each node its files and image in the inventory, and check its documents.

    [:octicons-arrow-right-24: Read](node-config.md)

-   :material-server-network: __Install on bare metal__

    ---

    Install chalkos on physical machines from the installer ISO.

    [:octicons-arrow-right-24: Read](install-bare-metal.md)

-   :material-monitor: __Run on KVM, Proxmox or libvirt__

    ---

    Run chalkos nodes as virtual machines on a KVM hypervisor.

    [:octicons-arrow-right-24: Read](kvm-proxmox-libvirt.md)

-   :material-harddisk: __Add storage volumes__

    ---

    Size VAR, and add encrypted or raw volumes on the system disk or on other disks.

    [:octicons-arrow-right-24: Read](storage-volumes.md)

-   :material-update: __Upgrade a cluster__

    ---

    Roll a new image through a cluster node by node, including a new Kubernetes version.

    [:octicons-arrow-right-24: Read](upgrade-cluster.md)

-   :material-database-arrow-down: __Registries and mirrors__

    ---

    Give nodes the credentials of a private registry, and pull images through a mirror.

    [:octicons-arrow-right-24: Read](registries.md)

-   :material-certificate: __Rotate certificates and CAs__

    ---

    Issue client files, renew node certificates, and rotate the cluster's CAs and keys.

    [:octicons-arrow-right-24: Read](rotate-certificates.md)

-   :material-lifebuoy: __Recover a node__

    ---

    Bring back a node that does not unlock, boot, renew its certificate or rejoin.

    [:octicons-arrow-right-24: Read](recover-node.md)

-   :material-stethoscope: __Troubleshooting__

    ---

    Find out why a node or the cluster misbehaves, starting from `chalkctl status`.

    [:octicons-arrow-right-24: Read](troubleshooting.md)

</div>
