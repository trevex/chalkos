---
title: "chalkctl"
description: "Build, sign, install and operate chalkos clusters"
---

# chalkctl

Build, sign, install and operate chalkos clusters

## Synopsis

chalkctl builds, signs, installs and operates the nodes of a chalkos cluster. It reads the
cluster definition from a flake, or from a manifest file with --manifest, and talks to chalkd on
each node over mutual TLS. Commands authenticate with the cluster's secrets file, which holds
every CA key, or with a client file (chalkctl config new), which holds a certificate of the
admin, operator or reader role. Each command's help says which of the two it needs. Commands that
accept both take --config, else --secrets, else the client file $CHALKOSCONFIG names, else a
secrets file in the flake directory, else ~/.config/chalkos/config.

A command's flags go after its name, anywhere among its arguments, and -- ends them: every
argument after it is read as a positional argument.

## Examples

```
  # Generate the cluster's secrets, install and bootstrap a control plane, write a kubeconfig.
  chalkctl gen secrets --recipient age1...
  chalkctl install cp1 --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
  chalkctl bootstrap cp1
  chalkctl kubeconfig
```

## Options

```
  -h, --help   help for chalkctl
```

## SEE ALSO

* [chalkctl apply-identity](chalkctl_apply-identity.md)	 - Deliver a node's identity from the cluster definition
* [chalkctl bootstrap](chalkctl_bootstrap.md)	 - Initialise the cluster on a control plane
* [chalkctl completion](chalkctl_completion.md)	 - Generate the autocompletion script for the specified shell
* [chalkctl config](chalkctl_config.md)	 - Manage client files
* [chalkctl disks](chalkctl_disks.md)	 - List a node's disks
* [chalkctl etcd](chalkctl_etcd.md)	 - Manage the cluster's etcd members
* [chalkctl gen](chalkctl_gen.md)	 - Generate files of a cluster
* [chalkctl install](chalkctl_install.md)	 - Install a node that waits in maintenance mode
* [chalkctl kubeconfig](chalkctl_kubeconfig.md)	 - Write an admin kubeconfig
* [chalkctl logs](chalkctl_logs.md)	 - Show a node's journal
* [chalkctl node](chalkctl_node.md)	 - Manage a node's certificate
* [chalkctl node-ca](chalkctl_node-ca.md)	 - Manage the node CA
* [chalkctl reboot](chalkctl_reboot.md)	 - Reboot a node
* [chalkctl recovery-key](chalkctl_recovery-key.md)	 - Print a node's recovery key
* [chalkctl rotate](chalkctl_rotate.md)	 - Rotate os-ca, kubernetes-ca, service-account-key or encryption-key
* [chalkctl sign](chalkctl_sign.md)	 - Sign the boot loader and UKIs of a disk image
* [chalkctl status](chalkctl_status.md)	 - Show an installed node's status
* [chalkctl storage](chalkctl_storage.md)	 - Manage a node's volumes
* [chalkctl upgrade](chalkctl_upgrade.md)	 - Install new images on the cluster's nodes, one control plane at a time

