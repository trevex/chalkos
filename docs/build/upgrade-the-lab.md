---
title: "Upgrade the lab to your own image"
description: "Build an image of your own from the lab template's flake, sign it and roll it through the quick start's lab"
---

# Upgrade the lab to your own image

This tutorial builds an image of your own from the `lab` template's flake, signs it with the lab's
Secure Boot key and upgrades both nodes of the quick start's lab to it, one node at a time. At the
end both nodes run your image and still hold the published one in their other slot. Most of the
time goes into the first build, while Nix downloads and builds the image's parts.

## Before you begin

You need the lab of [Quick start: Kubernetes](../get-started/quick-start-kubernetes.md), created
and running, with the config change of
[Change the nodes' config](../get-started/quick-start-kubernetes.md#change-the-nodes-config)
applied. Run every command of this page in the lab directory, in the shell `nix develop` opens, with
the three variables of
[Use the cluster with kubectl](../get-started/quick-start-kubernetes.md#use-the-cluster-with-kubectl)
exported; a new shell needs them exported again.

## Change the image

The published image holds the Kubernetes layer as chalkos ships it. A package, an extension or an
option of your own takes an image of your own, built with Nix from the template's flake. Its image,
`node-kvm`, starts from the same Kubernetes layer and carries the cluster's OS CA from
`secrets.pub.json`.

Any change to an image's modules makes a new image, which its store's root hash tells apart from
the one before; there is no version to raise. Edit `flake.nix` so the image `node-kvm` turns on
`chalkos.debug.tools`, which adds the tools `kubectl debug` uses on a node:

```diff
-            modules = [ chalkos.nixosModules.kubernetes ];
+            modules = [
+              chalkos.nixosModules.kubernetes
+              # The tools for kubectl debug.
+              { chalkos.debug.tools = true; }
+            ];
```

## Build the image

Commit the change and build the image into `images/node-kvm`. The flake passes its commit to
`mkImages` as `revision`, so chalkctl can say which commit each node runs. The first build takes
most of this tutorial, while Nix downloads and builds the image's parts; the
template's `.gitignore` keeps `images/` out of git:

```sh
git commit -am "Add the debug tools"
nix build .#node-kvm -o images/node-kvm
```

## Sign the image

The lab's firmware boots only images signed with the lab's Secure Boot key, and the build is
unsigned. [`chalklab sign`](../reference/cli/chalklab_sign.md) copies the image directory and
signs the copy with the lab's db key. The copy's directory must not exist yet, so remove the copy
from an earlier upgrade first:

```console
$ rm -rf images/node-kvm-signed
$ chalklab sign images/node-kvm --out images/node-kvm-signed
signed images/node-kvm-signed/chalkos.raw with the db key of the lab lab
```

## Roll the image through the nodes

A node changes its software only by booting a new image. An upgrade writes the new image into the
node's inactive slot and reboots into it; if the new image does not become healthy, the node falls
back to the image it ran before.

[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) with `--image` gives the signed copy to
the nodes and rolls it through the cluster, one node at a time. On its own, `--image` picks the
nodes that run an image of its name, `node-kvm`. The nodes run the published image, which names
itself `kubernetes-kvm`, so the command names them:

```console
$ chalkctl upgrade cp1 w1 --image images/node-kvm-signed --allow-downtime
cp1 runs the image kubernetes-kvm; it gets node-kvm
w1 runs the image kubernetes-kvm; it gets node-kvm
upgrading node-kvm on kvm to 7986f78938ed (186d48b7ef31): cp1, w1
cp1: installing 7986f78938ed (186d48b7ef31)
cp1: config unchanged
cp1: installed 7986f78938ed (186d48b7ef31), staged as .staged-chalkos_2.efi
cp1: cordoned, evicted 2 pods, kept 6
cp1: activated 7986f78938ed (186d48b7ef31), which boots next as chalkos_2+3.efi
cp1: rebooting into 7986f78938ed (186d48b7ef31)
cp1: runs 7986f78938ed (186d48b7ef31), found healthy
cp1: uncordoned
w1: installing 7986f78938ed (186d48b7ef31)
w1: config unchanged
w1: installed 7986f78938ed (186d48b7ef31), staged as .staged-chalkos_2.efi
w1: cordoned, evicted 2 pods, kept 2
w1: activated 7986f78938ed (186d48b7ef31), which boots next as chalkos_2+3.efi
w1: rebooting into 7986f78938ed (186d48b7ef31)
w1: runs 7986f78938ed (186d48b7ef31), found healthy
w1: uncordoned
upgraded 2 nodes to 7986f78938ed (186d48b7ef31)
```

`7986f78938ed` begins the new image's root hash, and `186d48b7ef31` the commit it was built from.

`--allow-downtime` is needed because the lab has a single control plane: while cp1 reboots, etcd
has no quorum and the API server is down, and chalkctl refuses that unless told to accept it. In
a cluster of three control planes, the other two keep etcd and the API server running while one
reboots.

An upgrade sends each node its config from the inventory together with the image. The
`config unchanged` line compares that config with the one the node runs: `config apply` delivered
the `TimeSync` already, so nothing changes.

Each node goes through the same steps. chalkctl sends the image to chalkd, which writes it into
the inactive slot and stages its boot entry. chalkctl drains the node, and chalkd activates the
entry with three tries, `chalkos_2+3.efi`, one generation above the entry the node booted, so
systemd-boot boots it first. The node reboots into the new image with
boot counting. Once its
health check passes, the boot is
blessed, which removes the counter, and chalkctl
uncordons the node. A node whose new image is not found healthy within its three tries boots the
image before again, and chalkctl stops there and shows what the failed boot logged.

## Check that it worked

`chalkctl status` shows that w1 booted the new image from the blessed entry, which has lost its
`+3`:

```console
$ chalkctl status w1
w1: 1 problem: image not the inventory's
config b8b41769a31866ce5c70011cfa1826b0668f39e9b2eac69d7fc0f847bae277fd (the inventory's)
platform kvm (the inventory's)
image 7986f78938ed (186d48b7ef31), booted from chalkos_2.efi
previous image c628af2ea405 (upgrade --rollback boots it)
its store's root hash is 7986f78938ed320bc54a75ad2ebd10329352f10e1fcb99c0fdeb25b034ad2f79, not that of the image node-kvm the inventory names, c628af2ea405dc1b9b319c555d15a3b3822953b0ae3cf8478927cb76bc074095; chalkctl upgrade w1 installs that image
VOLUME  DISK    MOUNT POINT  STATE
var     system  /var         mounted
OUTPUT            HASH          STATE
bgp               c7f2fa248b21  rendered
chrony            3b1ba1116223  rendered
cluster-registry  e3b0c44298fc  rendered
networkd          195ac3c4ec51  rendered
kubernetes worker: joined, node ready: True
kubernetes versions: API server 1.37.2, kubelet 1.37.2
certificates:
  node                expires 2027-11-20
  OS CA               expires 2036-11-17
  Kubernetes CA       expires 2036-11-17
  kubelet serving CA  expires 2036-11-17
  kubelet client      expires 2027-11-20
  kubelet serving     expires 2027-11-20
trust:
  OS CA          fe3339dfcf022d78
  Kubernetes CA  1009654922c03a67
time: synchronised to 162.159.200.1, offset +0.000000 s
```

A worker holds fewer certificates than a control plane: it issues none, so it holds no node CA and
no CA keys. Its time source is `time.cloudflare.com`, at the address `162.159.200.1`, from the quick start's
config change. The
published image it ran before stays in the other slot, which `chalkctl upgrade --rollback` boots.

The lab's inventory still names the published image, so `chalkctl status` says that the node runs
another one. To make your build the lab's image, point `node-kvm` in `chalkos.yaml` at its
directory, `node-kvm: {source: {directory: images/node-kvm}}`, and create the lab again, as the
template's `README.md` describes. This step is optional and starts over: `chalklab destroy` removes
the lab's disks, so the nodes lose the published image in their other slot and cannot roll back to
it.

Both nodes are `Ready` in Kubernetes again:

```console
$ kubectl get nodes
NAME   STATUS   ROLES    AGE     VERSION
cp1    Ready    <none>   5m30s   v1.37.2
w1     Ready    <none>   5m23s   v1.37.2
```

The new image has the debugging tools, so a debugging pod can run the node's own `cat` on its
files. `kubectl debug` starts one on w1, which prints the node's os-release and stops. The pod's
log names the image and its platform:

```console
$ kubectl debug node/w1 --image=busybox -- chroot /host /run/current-system/sw/bin/cat /etc/os-release
Creating debugging pod node-debugger-w1-42xwg with container debugger on node w1.
$ kubectl logs node-debugger-w1-42xwg
...
CHALKOS_BOOT_TRIES=3
CHALKOS_IMAGE="node-kvm"
CHALKOS_PLATFORM=kvm
...
IMAGE_ID=chalkos
IMAGE_VERSION=""
...
```

## What next

- [Your first image flake](first-image.md) explains the flake that built the image.
- [Write your first extension](first-extension.md) adds a config kind of your own to the lab's
  image.
- [Upgrade nodes](../operate/upgrade.md) covers upgrades of real clusters, and what to do when a
  node rolls back.
- [`chalklab destroy`](../reference/cli/chalklab_destroy.md) removes the lab when you are done.
