---
title: "Operate nodes"
description: "Install, upgrade, recover and troubleshoot chalkos nodes"
---

# Operate nodes

These pages are for operators who install and run chalkos nodes with chalkctl, YAML files and
signed images. They assume the shipped variant, which runs Kubernetes on each node;
[Run Kubernetes](../kubernetes/index.md) adds the steps that set up and run the cluster.

## Set up

These pages go from a new workstation to the first installed node.

<div class="grid cards" markdown>

-   :material-laptop: __Set up your workstation__

    ---

    Get chalkctl, generate the cluster's secrets and choose the credentials each command needs.

    [:octicons-arrow-right-24: Read](workstation.md)

-   :material-file-cog: __Write a node's config__

    ---

    Give each node its files and image in the inventory, and check its documents.

    [:octicons-arrow-right-24: Read](node-config.md)

-   :material-server-network: __Install a node__

    ---

    Boot a physical machine or a VM into maintenance mode and install a node on it.

    [:octicons-arrow-right-24: Read](install.md)

</div>

## Day 2

These pages cover nodes that run: config changes, new and removed nodes, upgrades, access,
storage and registries.

<div class="grid cards" markdown>

-   :material-file-edit: __Change a node's config__

    ---

    Deliver changed YAML to a running node, preview it, and act on a refusal.

    [:octicons-arrow-right-24: Read](change-config.md)

-   :material-server-plus: __Add and remove nodes__

    ---

    Add a node to a running cluster, reinstall one, or take one out for good.

    [:octicons-arrow-right-24: Read](add-remove-nodes.md)

-   :material-update: __Upgrade nodes__

    ---

    Roll a new image through the nodes, and act on a node that rolls back.

    [:octicons-arrow-right-24: Read](upgrade.md)

-   :material-certificate: __Manage access and certificates__

    ---

    Give and revoke access, renew node certificates, and rotate the OS CA and the node CA.

    [:octicons-arrow-right-24: Read](access.md)

-   :material-harddisk: __Add storage volumes__

    ---

    Size VAR, and add encrypted or raw volumes on the system disk or on other disks.

    [:octicons-arrow-right-24: Read](storage-volumes.md)

-   :material-database-arrow-down: __Registries and mirrors__

    ---

    Give nodes the credentials of a private registry, and pull images through a mirror.

    [:octicons-arrow-right-24: Read](registries.md)

</div>

## When things go wrong

These pages read what a node does and bring back a node that fails.

<div class="grid cards" markdown>

-   :material-text-box-search: __Status, logs and the console__

    ---

    Read what `chalkctl status` reports, follow a unit's journal and reach a node's tools.

    [:octicons-arrow-right-24: Read](status-and-logs.md)

-   :material-lifebuoy: __Recover a node__

    ---

    Bring back a node that does not unlock, boot or renew, or whose secrets file is lost.

    [:octicons-arrow-right-24: Read](recover-node.md)

-   :material-stethoscope: __Troubleshooting__

    ---

    Find the cause of a node's symptom and the page that fixes it.

    [:octicons-arrow-right-24: Read](troubleshooting.md)

</div>
