---
title: "Operate nodes"
description: "Install, upgrade, recover and troubleshoot chalkos nodes"
---

# Operate nodes

These pages are for operators who install and run chalkos nodes with chalkctl, YAML files and
signed images. They apply to every node, apart from the sections on etcd, the API server and
kubeconfigs, which need the Kubernetes layer. Their examples come from a Kubernetes cluster.
[Run Kubernetes](../kubernetes/index.md) sets up and runs the cluster itself, and
[Run a router](../router/index.md) covers what router nodes add.
The [command cheat sheet](../reference/cheat-sheet.md) lists the command and the credential for
each common task.

## Read in this order

1. [Set up your workstation](workstation.md) gets chalkctl and the cluster's secrets.
2. [Write a node's config](node-config.md) gives each node its files and image.
3. [Install a node](install.md) installs the first machine.
4. [Change a node's config](change-config.md) delivers a change to a running node.
5. [Upgrade nodes](upgrade.md) rolls a new image through the nodes.
6. [Status, logs and the console](status-and-logs.md) reads what a node does.

## Set up

These pages go from a new workstation to the first installed node.

<!-- docgen cards: workstation.md secrets.md node-config.md install.md install-vm.md secure-boot.md -->
<div class="grid cards" markdown>

-   __[Set up your workstation](workstation.md)__

    ---

    Get chalkctl, lay out the repository, generate the cluster's secrets and pick each command's credentials

-   __[Manage secrets](secrets.md)__

    ---

    Generate and rotate the cluster secrets file and source service credentials from local files, age, or SOPS

-   __[Write a node's config](node-config.md)__

    ---

    Give nodes their config files and images in the inventory, write the built-in kinds and validate them

-   __[Install a node](install.md)__

    ---

    Boot a physical machine from the installer and install a node of the inventory on it with chalkctl install

-   __[Install a node in a VM](install-vm.md)__

    ---

    Run a node of the inventory as a VM under QEMU, libvirt or Proxmox, from the kvm image, and install it in place

-   __[Enrol Secure Boot keys](secure-boot.md)__

    ---

    Enrol the cluster's Secure Boot keys in a machine's firmware with chalkctl secureboot enroll, or through its setup screen or BMC

</div>
<!-- end docgen cards -->

## Day 2

These pages cover nodes that run: config changes, new and removed nodes, upgrades, access,
storage, registries and the tasks that recur.

<!-- docgen cards: change-config.md add-remove-nodes.md upgrade.md access.md storage-volumes.md registries.md recurring-tasks.md -->
<div class="grid cards" markdown>

-   __[Change a node's config](change-config.md)__

    ---

    Deliver changed YAML to a running node with chalkctl config apply, preview it with --diff and --dry-run, try it with --try and act on a refusal

-   __[Add and remove nodes](add-remove-nodes.md)__

    ---

    Add a node to the inventory and install it, reinstall a node, take it out of the cluster for good, or reuse its name

-   __[Upgrade nodes](upgrade.md)__

    ---

    Roll a new image through the nodes with chalkctl upgrade, and act on a node that rolls back

-   __[Manage access and certificates](access.md)__

    ---

    Give and revoke access to the nodes, keep node certificates valid, and rotate the OS CA and the node CA

-   __[Add storage volumes](storage-volumes.md)__

    ---

    Size VAR and add encrypted or raw volumes on the system disk or on other disks

-   __[Registries and mirrors](registries.md)__

    ---

    Give nodes the credentials of a private registry, and pull images through a mirror

-   __[Recurring tasks](recurring-tasks.md)__

    ---

    What expires when, how to check it, what to back up, and how to move a cluster to a newer chalkos

</div>
<!-- end docgen cards -->

## Diagnose and recover

These pages read what a node does and bring back a node that fails.

<!-- docgen cards: status-and-logs.md console.md recover-node.md troubleshooting.md -->
<div class="grid cards" markdown>

-   __[Status, logs and the console](status-and-logs.md)__

    ---

    Read what chalkctl status reports, follow a unit's journal, run the image's diagnostics, watch the console and reach a node's tools

-   __[The console on the machine's screen](console.md)__

    ---

    Read a node at its screen: its identity and health, its status, network, storage, logs and layers, and the actions for a node chalkctl cannot reach

-   __[Recover a node](recover-node.md)__

    ---

    Bring back a node that does not unlock, boot or renew, or whose secrets file is lost

-   __[Troubleshooting](troubleshooting.md)__

    ---

    Find the cause of a node's symptom and the page that fixes it

</div>
<!-- end docgen cards -->
