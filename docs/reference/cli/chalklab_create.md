---
title: "chalklab create"
description: "Create a lab of the cluster's nodes, install them and bootstrap the cluster"
---

# chalklab create

Create a lab of the cluster's nodes, install them and bootstrap the cluster

## Synopsis

Creates the lab of a cluster and brings the cluster up. chalklab builds the kvm image of each
role, creates the lab's Secure Boot keys and enrolls them in the VMs' firmware, signs copies of
the images with them, gives each node a disk backed by its role's image and starts the
supervisor, which runs a TPM and a VM per node. Each node boots its image in maintenance mode;
chalklab reads chalkd's certificate fingerprint from the console and installs the node in place
with chalkctl install --fingerprint. When the lab has a control plane, chalklab bootstraps the
first one and writes a kubeconfig that reaches its API server through the forwarded port. Last,
it writes an admin client file for chalkctl that reaches the nodes through their forwarded ports.

The command needs the cluster's secrets file, which chalkctl reads to install the nodes and to
write the kubeconfig and the client file: --secrets, else secrets.age or secrets.json in the
flake directory.

Only nodes on the kvm platform run in a lab, and each needs exactly one MAC address in its
network definition, which the lab's network interface gets. --nodes runs some of them. With
--manifest the cluster's manifest comes from a file, so every role's image must be given with
--image.

```
chalklab create [flags]
```

## Examples

```
  chalklab create

  # A smaller lab of two nodes of a flake that defines several clusters.
  chalklab create --cluster lab --nodes cp1,w1 --controlplane-memory 2048 --memory 1024

  # Make a registry on the host's port 5000 reachable at 10.0.2.100:5000 in the VMs.
  chalklab create --guest-forward 10.0.2.100:5000=127.0.0.1:5000
```

## Options

```
      --cluster string            cluster to use when the flake defines several
      --controlplane-memory int   memory of a control plane's VM, in MiB (default 3072)
      --cpus int                  virtual CPUs of each VM (default 2)
      --disk-size string          size of each VM's sparse disk (default "16G")
      --flake string              directory of the flake that defines the cluster (default ".")
      --guest-forward strings     GUEST=HOST: make a host address (a local registry, for example) reachable at a guest address of the VMs' user-mode network; may be repeated
  -h, --help                      help for create
      --image strings             ROLE=DIR: the kvm image of a role (the directory nix build makes) instead of building it; may be repeated
      --manifest string           read the cluster's manifest from this file instead of evaluating the flake; every role needs --image then
      --memory int                memory of the other VMs, in MiB (default 2048)
      --nodes string              comma-separated nodes to run (default every node of the cluster)
      --secrets string            secrets file chalkctl reads (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalklab](chalklab.md)	 - Run a chalkos cluster's nodes as QEMU virtual machines on this machine

