---
title: "chalklab create"
description: "Create a lab of the cluster's nodes, install them and bootstrap the cluster"
---

# chalklab create

```sh
chalklab create [flags]
```

Creates the lab of a lab file and brings the cluster up. chalklab reads the lab file
(chalklab.yaml, or --lab) and the inventory it names; each node the lab file names runs the kvm
image the inventory names for it. chalklab creates the lab's Secure Boot keys and enrolls them in
the VMs' firmware, copies each node's image and signs the copy with them, gives each node a disk
backed by it and the empty disks the lab file adds, and starts the supervisor, which runs a TPM
and a VM per node. Each VM's NIC on the lab network gets the MAC address the lab file gives it,
which the node's Network document must match; chalklab does not read the Network documents.
A node's memory, CPUs and disks default by the type of its Kubernetes Node: 3072 MiB for a control
plane, 2048 MiB for another node, 2 CPUs and one disk of 16G.

chalklab first writes an admin client file. Each node boots its image in maintenance mode;
chalklab reads chalkd's certificate fingerprint from the console and installs the node in place
with chalkctl install --fingerprint. When the lab has a control plane, chalklab bootstraps the
first one and writes a kubeconfig that reaches its API server through the forwarded port.
chalkctl reads the lab's inventory, chalkos.yaml in the lab's state directory: the lab file's
inventory with its nodes at their forwarded ports. It names no secrets file: chalklab passes
chalkctl --secrets. The closing output exports the client file as $CHALKOSCONFIG, which chalkctl
reads when a command names no --config or --secrets, so the commands it prints name neither.

The command needs the cluster's secrets file, --secrets, which chalkctl reads to install the
nodes and to write the kubeconfig and the client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--guest-forward`{ #flag-guest-forward } | list of string | — | GUEST=HOST: make a host address (a local registry, for example) reachable at a guest address of the VMs' user-mode network; may be repeated. |
| `--lab`{ #flag-lab } | string | chalklab.yaml in the current directory | Lab file naming the inventory and the nodes the lab runs. |
| `--secrets`{ #flag-secrets } | string | required | The cluster's secrets file, which chalkctl reads to install the nodes. |

## Examples

```sh
chalklab create --secrets secrets.age

# Make a registry on the host's port 5000 reachable at 10.0.2.100:5000 in the VMs.
chalklab create --secrets secrets.age --guest-forward 10.0.2.100:5000=127.0.0.1:5000
```

## See also

- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
