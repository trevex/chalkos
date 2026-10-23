---
title: "Operate nodes"
description: "Install, upgrade, recover and troubleshoot chalkos nodes"
---

# Operate nodes

These pages are for operators who run nodes with chalkctl and YAML files. They cover installing
nodes, adding storage, upgrading, rotating certificates, recovering a node and troubleshooting.
Pages here hold for every node; [Run Kubernetes](../kubernetes/index.md) adds what a Kubernetes
cluster needs.

<div class="grid cards" markdown>

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
