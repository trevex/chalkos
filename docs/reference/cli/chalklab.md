---
title: "chalklab"
description: "Run a chalkos cluster's nodes as QEMU virtual machines on this machine"
---

# chalklab

Run a chalkos cluster's nodes as QEMU virtual machines on this machine

## Synopsis

chalklab runs the nodes of a chalkos cluster as QEMU virtual machines on this machine, each with
Secure Boot and a TPM. A lab file (chalklab.yaml) names the cluster's inventory and the nodes the
lab runs, each with the MAC address of its NIC on a private network between the VMs. Each node's
chalkd and each control plane's API server are forwarded to a port on 127.0.0.1. A supervisor
process keeps the VMs running in the background. A lab's state (its disks, keys, console logs,
kubeconfig, client file and the inventory that reaches the nodes at their forwarded ports) lives
in $XDG_STATE_HOME/chalklab/&lt;cluster>, by default ~/.local/state/chalklab/&lt;cluster>. chalklab
needs /dev/kvm, which most distributions grant to the kvm group. It never builds images. Only
create needs the cluster's secrets file; the other commands work on the lab's state alone.

## Examples

```
  # In a directory made from the lab template, once its image is built.
  chalklab create --secrets secrets.age
  chalklab status
  chalklab destroy
```

## Options

```
  -h, --help   help for chalklab
```

## SEE ALSO

* [chalklab completion](chalklab_completion.md)	 - Generate the autocompletion script for the specified shell
* [chalklab console](chalklab_console.md)	 - Follow a node's serial console
* [chalklab create](chalklab_create.md)	 - Create a lab of the cluster's nodes, install them and bootstrap the cluster
* [chalklab destroy](chalklab_destroy.md)	 - Stop the lab and remove its state
* [chalklab sign](chalklab_sign.md)	 - Sign an image with the lab's Secure Boot keys, for upgrades
* [chalklab start](chalklab_start.md)	 - Start a stopped lab, or the VMs of a running lab that stopped
* [chalklab status](chalklab_status.md)	 - Show the lab's VMs, ports and files

