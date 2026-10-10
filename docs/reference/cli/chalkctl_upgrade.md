---
title: "chalkctl upgrade"
description: "Install new images on the cluster's nodes, one control plane at a time"
---

# chalkctl upgrade

Install new images on the cluster's nodes, one control plane at a time

## Synopsis

Installs on each node the image of its role and platform: control planes one at a time, each
only while etcd keeps its quorum without it, then workers and nodes without Kubernetes in batches
of --max-unavailable. Without --image the nodes (every node of the cluster, or those --nodes
names) are grouped by the role and platform they run, and each group's image is built from the
cluster definition; with --image the nodes of the image's role and platform get it, and those of
its role on another platform are skipped and named. A node that runs on another platform than
the cluster definition declares stops the run before any node is sent anything.

A node gets the image in its inactive slot, is cordoned and drained within its pods'
PodDisruptionBudgets, reboots into the image, and is uncordoned once the boot was found healthy
and the node is Ready. A node whose image never becomes healthy falls back to the image before,
and the run stops there, showing what that boot logged; with --retry-failed a node that fell
back from the image before gets it once more.

Run again, the command skips nodes that run the image and continues one it stopped at; it
uncordons only nodes it cordoned itself. etcd of one or two control planes loses its quorum while
one reboots, and the API server is down, which --allow-downtime accepts. Pods with emptyDir
volumes are evicted only with --delete-emptydir-data. The command needs an operator client file
or the secrets file. Without --image it builds the images from the flake, so it runs in the
flake's directory or with --flake.

```
chalkctl upgrade [flags]
```

## Examples

```
  # Build each node's image from the flake and upgrade the whole cluster.
  chalkctl upgrade

  # Upgrade the workers of an image's role, two at a time, with a prebuilt image, which
  # chalkctl signs.
  chalkctl upgrade --image ./worker-image --max-unavailable 2 --sign-key db.key --sign-cert db.crt
```

## Options

```
      --allow-downtime         upgrade one or two control planes, whose etcd loses its quorum and API server is down while one reboots
      --cluster string         cluster to use when the flake defines several
      --config string          client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)
      --delete-emptydir-data   evict pods with emptyDir volumes too, deleting their data
      --endpoint NODE=ADDR     address of a node's chalkd, NODE=ADDR, host or host:port; may be repeated (default each node's first static address, or the address a client file that prefers its addresses names)
      --flake string           directory of the flake that defines the cluster (default ".")
  -h, --help                   help for upgrade
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --image string           image to install on the nodes of its role and platform: a raw image with repart-output.json next to it, or the directory nix build produces (default: build each node's image from the cluster definition)
      --manifest string        read the cluster's manifest from this file instead of evaluating the flake
      --max-unavailable int    how many workers and nodes without Kubernetes upgrade at once (default 1)
      --no-reboot              install the images on the nodes without draining or rebooting them; they boot with their next reboot
      --nodes string           comma-separated nodes to upgrade (default every node of the cluster, or with --image every node of the image's role)
      --retry-failed           install an image again, once, on nodes that fell back from it
      --secrets string         secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
      --sign-cert string       PEM certificate of the Secure Boot db signer
      --sign-key string        PEM key of the Secure Boot db signer, to sign the images' UKIs
      --timeout duration       how long to wait for each node to come back healthy and for its drain (default 30m0s)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

