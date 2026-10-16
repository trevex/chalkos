---
title: "chalklab create"
description: "Create a lab of the cluster's nodes, install them and bootstrap the cluster"
---

# chalklab create

Create a lab of the cluster's nodes, install them and bootstrap the cluster

## Synopsis

Creates the lab of a lab file and brings the cluster up. chalklab reads the lab file
(chalklab.yaml, or --lab) and the inventory it names; each node the lab file names runs the kvm
image its Image document names. chalklab creates the lab's Secure Boot keys and enrolls them in
the VMs' firmware, copies each node's image and signs the copy with them, gives each node a disk
backed by it and the empty disks the lab file adds, and starts the supervisor, which runs a TPM
and a VM per node. Each VM's NIC on the lab network gets the MAC address the lab file gives it,
which the node's Network document must match; chalklab does not read the Network documents.
A node's memory, CPUs and disks default by its Node's type: 3072 MiB for a control plane, 2048 MiB
for another node, 2 CPUs and one disk of 16G.

Each node boots its image in maintenance mode; chalklab reads chalkd's certificate fingerprint
from the console and installs the node in place with chalkctl install --fingerprint. When the
lab has a control plane, chalklab bootstraps the first one and writes a kubeconfig that reaches
its API server through the forwarded port. Last, it writes an admin client file. chalkctl reads
the lab's inventory, chalkos.yaml in the lab's state directory: the lab file's inventory with
its nodes at their forwarded ports. It names no secrets file: chalklab passes chalkctl --secrets,
and --sign-cert with the lab's db certificate.

The command needs the cluster's secrets file, --secrets, which chalkctl reads to install the
nodes and to write the kubeconfig and the client file.

```
chalklab create [flags]
```

## Examples

```
  chalklab create --secrets secrets.age

  # Make a registry on the host's port 5000 reachable at 10.0.2.100:5000 in the VMs.
  chalklab create --secrets secrets.age --guest-forward 10.0.2.100:5000=127.0.0.1:5000
```

## Options

```
      --guest-forward strings   GUEST=HOST: make a host address (a local registry, for example) reachable at a guest address of the VMs' user-mode network; may be repeated
  -h, --help                    help for create
      --lab string              lab file naming the inventory and the nodes the lab runs (default chalklab.yaml in the current directory)
      --secrets string          the cluster's secrets file, which chalkctl reads to install the nodes
```

## SEE ALSO

* [chalklab](chalklab.md)	 - Run a chalkos cluster's nodes as QEMU virtual machines on this machine

