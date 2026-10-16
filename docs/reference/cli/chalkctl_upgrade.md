---
title: "chalkctl upgrade"
description: "Install new images on the cluster's nodes, one control plane at a time"
---

# chalkctl upgrade

Install new images on the cluster's nodes, one control plane at a time

## Synopsis

Installs on each node a new image: control planes one at a time, each only while etcd keeps its
quorum without it, then workers and nodes without Kubernetes in batches of --max-unavailable.
Without --image each node (every node of the cluster, or those --nodes names) gets the image its
Image document names, and a node that runs on another platform than that image stops the run
before any node is sent anything. An image directory whose image has another root hash than its
metadata.json names, which status compares nodes with, is refused: rebuild the image or link its
directory again. With --image the nodes that run the image's variant get it, or
those --nodes names whatever they run, and nodes on another platform are skipped and named. A
node may get an image of another variant than the one it runs, which chalkctl prints; the node
checks its stored config against the image before the image becomes its next boot.

A node gets the image in its inactive slot, is cordoned and drained within its pods'
PodDisruptionBudgets, reboots into the image, and is uncordoned once the boot was found healthy
and the node is Ready. A node whose image never becomes healthy falls back to the image before,
and the run stops there, showing what that boot logged; with --retry-failed a node that fell
back from the image before gets it once more.

Run again, the command skips nodes that run the image and continues one it stopped at; it
uncordons only nodes it cordoned itself. etcd of one or two control planes loses its quorum while
one reboots, and the API server is down, which --allow-downtime accepts. Pods with emptyDir
volumes are evicted only with --delete-emptydir-data. The command needs an operator client file
or the secrets file.

```
chalkctl upgrade [flags]
```

## Examples

```
  # Upgrade the whole cluster, each node to the image its Image names.
  chalkctl upgrade

  # Upgrade the nodes that run an image's variant, two at a time, with a prebuilt image, which
  # chalkctl signs.
  chalkctl upgrade --image ./worker-image --max-unavailable 2 --sign-key db.key --sign-cert db.crt
```

## Options

```
      --address NODE=ADDR      address of a node's chalkd, NODE=ADDR, host or host:port; may be repeated (default each node's address in the inventory)
      --allow-downtime         upgrade one or two control planes, whose etcd loses its quorum and API server is down while one reboots
      --config string          client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
      --delete-emptydir-data   evict pods with emptyDir volumes too, deleting their data
  -h, --help                   help for upgrade
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --image string           image to install on the nodes that run its variant, or on the nodes --nodes names whatever they run: a raw image with repart-output.json next to it, or an image directory (default each node's Image's directory)
      --inventory string       inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --manifest string        read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --max-unavailable int    how many workers and nodes without Kubernetes upgrade at once (default 1)
      --no-reboot              install the images on the nodes without draining or rebooting them; they boot with their next reboot
      --nodes string           comma-separated nodes to upgrade (default every node of the cluster, or with --image every node that runs the image's variant)
      --retry-failed           install an image again, once, on nodes that fell back from it
      --secrets string         secrets file, encrypted with age or not, or - to read it from standard input
      --sign-cert string       PEM certificate of the Secure Boot db signer, to check the images' signatures against before they are sent
      --sign-key string        PEM key of the Secure Boot db signer, to sign the images' UKIs
      --timeout duration       how long to wait for each node to come back healthy and for its drain (default 30m0s)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Sign, install and operate chalkos clusters

