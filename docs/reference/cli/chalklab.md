---
title: "chalklab"
description: "Run a chalkos cluster's nodes as QEMU virtual machines on this machine"
---

# chalklab

Run a chalkos cluster's nodes as QEMU virtual machines on this machine

## Synopsis

chalklab runs the nodes of a chalkos cluster as QEMU virtual machines on this machine, each with
Secure Boot and a TPM. A lab is built from the cluster definition in a flake: its kvm nodes run
on a private network that matches their static addresses, and each node's chalkd and each
control plane's API server are forwarded to a port on 127.0.0.1. A supervisor process keeps the
VMs running in the background. A lab's state, its disks, keys, console logs, kubeconfig and
client file, lives in $XDG_STATE_HOME/chalklab/&lt;cluster>, by default
~/.local/state/chalklab/&lt;cluster>. chalklab needs /dev/kvm, which most distributions grant to the
kvm group.

## Examples

```
  # In the directory of a flake made from the lab template.
  chalklab create
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

