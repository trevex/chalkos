---
title: "Build an air-gapped cluster"
description: "Run a chalkos cluster without internet access: what to bring in, in which order, and how to upgrade it later"
---

# Build an air-gapped cluster

This guide runs a chalkos cluster on a network without internet access. Everything the nodes would
fetch from the internet comes in from a connected machine instead: the OS images, chalkctl, the
container images and the time. At the end the cluster runs, every node pulls its container images
from the control planes, and its clocks follow a time server inside the air gap. The last sections
cover upgrades, new images and the limits.

The guide uses the cluster of [Build a cluster on real machines](cluster-on-machines.md): the
`cluster-metal` template's three control planes `cp1`, `cp2` and `cp3` at `192.168.1.11` to
`192.168.1.13`, with the network's router at `192.168.1.1`. Read that guide first; this one lists
what changes. The [home lab](home-lab.md#run-the-lab-without-internet-access-optional) has a
variant that cuts a running lab off the internet.

## What comes in from outside

A chalkos cluster takes four things from the internet by default. In an air gap, each comes from
somewhere else:

| What | Default source | In the air gap |
| --- | --- | --- |
| OS images and the installer | built with Nix, which downloads the sources | built on a connected machine and carried in as image directories |
| chalkctl | the chalkos flake | its static program, copied from a connected machine |
| Container images | the registries the `Cluster` document names | the cluster registry on the control planes, filled with `chalkctl registry push` |
| Time | `ptbtime1.ptb.de` to `ptbtime3.ptb.de` over NTS | a time server inside the air gap, named in a `TimeSync` document |

chalkctl needs no internet. It reaches the nodes at the addresses of the inventory, reads the image
directories the inventory names, and generates and holds the cluster's secrets on the workstation.
The installer holds no config and fetches nothing: `chalkctl install` sends the image to it.

The nodes need no DNS names either, as long as every address chalkos uses is an IP address: the
inventory's, the `ClusterRegistry`'s endpoints and the `TimeSync`'s servers. Names outside the
cluster do not resolve, in pods or on the nodes. CoreDNS forwards every name outside the cluster
to the servers of the node's resolv.conf, and refuses to start when the file names none, so keep
a resolver in the air gap that the nodes' `Network` documents name, such as the network's
router.

## Before you begin

- You followed [Build a cluster on real machines](cluster-on-machines.md) up to its install, or
  know its steps.
- A connected machine with Nix builds the images, as the
  [build host](../get-started/requirements.md#build-host) describes, and has
  [crane](https://github.com/google/go-containerregistry/tree/main/cmd/crane) and yq.
- The workstation inside the air gap has chalkctl, mtools, kubectl and yq. Copy chalkctl's
  program as [Requirements](../get-started/requirements.md#workstation) describes.
- A time server answers NTP inside the air gap, such as a clock with a GPS receiver or a machine
  that runs chrony as a server. chalkos nodes run no NTP server for other machines.
- A way to carry files into the air gap, such as a USB disk.

## Prepare on a connected machine

In the repository of the cluster, build the image and the installer as
[Build the image and the installer](cluster-on-machines.md#build-the-image-and-the-installer)
describes, with the cluster's OS CA in `osCA`:

```sh
nix build .#node-metal -o images/node-metal
nix build .#installer -o installer
```

Then copy the container images. The `Cluster` document's `images` block names every image the
control plane, CoreDNS, the pod sandbox and flannel run, each pinned by digest. Copy each into an
OCI image layout of its own with crane, as
[Bring the images into the air gap](../kubernetes/cluster-registry.md#bring-the-images-into-the-air-gap)
explains:

```sh
mkdir -p images/oci
for ref in $(yq 'select(.kind == "Cluster") | .spec.images[]' cluster/cluster.yaml); do
  repo=${ref%%@*}; name=${repo##*/}; name=${name%%:*}
  rm -rf "images/oci/$name"
  crane pull --format=oci "$ref" "images/oci/$name"
done
```

Copy the images of your workloads, and those that the objects of your `Addon` documents name, the
same way, each into a directory of its own.

Copy the repository to the disk you carry in, `<carry>`, with the builds as directories rather
than the links `nix build` makes, and chalkctl's program beside it:

```sh
cp -rL . <carry>/cluster
cp "$(nix build --no-link --print-out-paths github:trevex/chalkos/<rev>#chalkctl)/bin/.chalkctl-wrapped" \
  <carry>/chalkctl
```

`<rev>` is the chalkos revision that `flake.lock` pins, so chalkctl and the image match. The copy
holds the inventory, the config files, `secrets.age`, `secrets.pub.json`, the image, the installer
and the container images.

## Give the nodes a time server

A node checks every certificate against its clock, and the default time servers are on the
internet. Name the time server inside the air gap in a `TimeSync` document that every node gets:

```yaml title="cluster/time.yaml"
apiVersion: time.chalkos.dev/v1alpha1
kind: TimeSync
metadata: {name: default}
spec:
  servers:
    - {host: 192.168.1.1, nts: false}
```

Replace `192.168.1.1` with your time server's address. `nts: false` takes plain NTP, which most
local time servers serve; leave it out for a server with NTS. A node without a reachable server
keeps its clock running from where it stands, and its clock drifts.

## Turn the cluster registry on

The cluster registry runs on the control planes and serves the container images to every node.
Add a `ClusterRegistry` that every Kubernetes node gets, with an address of every control plane and
every host the cluster's images come from:

```yaml title="cluster/cluster-registry.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: ClusterRegistry
metadata: {name: cluster}
spec:
  mirrors: [docker.io, ghcr.io, registry.k8s.io]
  endpoints: [192.168.1.11, 192.168.1.12, 192.168.1.13]
```

[Write the ClusterRegistry](../kubernetes/cluster-registry.md#write-the-clusterregistry)
explains the fields. Check the files against the image:

```sh
chalkctl validate
```

## Install the nodes

Install the three nodes as [Build a cluster on real machines](cluster-on-machines.md) describes.
The inventory's image is a directory, `images/node-metal`, which chalkctl reads without a network.
Each control plane starts the cluster registry at its first boot, and every node follows the time
server of the `TimeSync`. Do not bootstrap yet: the control plane's images are not in the registry.

## Push the images and bootstrap

Push every image of the `Cluster` document to every endpoint, from the layouts you carried in:

```sh
for ref in $(yq 'select(.kind == "Cluster") | .spec.images[]' cluster/cluster.yaml); do
  repo=${ref%%@*}; name=${repo##*/}; name=${name%%:*}
  chalkctl registry push "images/oci/$name" "$ref" --secrets secrets.age
done
```

chalkctl refuses a layout whose digest is not the one the reference pins, so a changed copy never
reaches the registry. Check that every endpoint holds every image:

```sh
chalkctl registry list --secrets secrets.age
```

Every line ends in `all`. Then bootstrap the cluster and write a kubeconfig, as
[Bootstrap Kubernetes](cluster-on-machines.md#bootstrap-kubernetes) describes. The control planes
pull their images from the cluster registry.

## Cut the connection

A cluster prepared on a connected network may have reached the internet while you set it up. Cut
that path now, on the network's router or firewall, and leave the workstation's path to the nodes.
The cluster needs nothing from outside afterwards: its images are in the registry, and its time and
names come from inside the air gap.

## Check that it worked

The nodes are `Ready` and the cluster's pods run:

```sh
kubectl get nodes
kubectl get pods -A
```

`chalkctl registry list` shows `all` for every image and exits 0. `chalkctl status cp1` prints a
line `time:` that names the air gap's time server as the one the clock follows. A pod whose image
is not in the registry stays in `ImagePullBackOff`, because containerd finds it neither in the
cluster registry nor at its host.

## Upgrade and change the cluster later

Every change that brings something new starts on the connected machine.

### Upgrade the OS image

Build the new image on the connected machine, copy its directory into the air gap, and point the
inventory's image at it. Then roll it out with `chalkctl upgrade`, as
[Upgrade nodes](../operate/upgrade.md) describes. chalkctl reads the image from the directory and
sends it to each node.

### Move to a new Kubernetes release

Copy the new release's images with crane, carry them in, and push them before you deliver the
`Cluster` document that names them. A control plane cannot start an image the registry lacks, so
its trial ends in a rollback. [Upgrade Kubernetes](../kubernetes/upgrade-kubernetes.md) describes
both steps of a release: the control plane's images through config, then the kubelets through a
new OS image.

### Add and remove images

Copy a workload's image, carry it in and push it before you deploy the pods that run it, as
[Push the workloads' images](../kubernetes/cluster-registry.md#push-the-workloads-images)
describes. Delete images you no longer run with `chalkctl registry delete`, and free their space
with `chalkctl registry gc`, as
[Delete images and free space](../kubernetes/cluster-registry.md#delete-images-and-free-space)
describes. Keep the images of the previous release until the new one has proven itself: a rollback
of the `Cluster` document names them again, and a node that lacks them pulls them.

### Add a control plane

Add its address to the `ClusterRegistry`'s endpoints, install it, and push every image again, as
[Add or remove a control plane](../kubernetes/cluster-registry.md#add-or-remove-a-control-plane)
describes.

## Limits

- Nothing outside the cluster registry's `mirrors` reaches the nodes. A pod that names an image of
  another host fails to pull it.
- chalkd does not pull OS images from the cluster registry. An inventory image with an `oci`
  source needs a registry that the workstation and the nodes reach; the air gap takes image
  directories instead.
- The copies take more space than the nodes run: crane copies every platform of an index. The
  `cluster-metal` template's eight images take about 1.2 GiB.
- A time server without a reference clock of its own keeps the nodes' clocks together, but they
  drift from real time. Certificates that other systems issue, and those chalkos issues for them,
  are checked against these clocks.
- Names outside the cluster do not resolve. Anything that needs one, such as a webhook of an
  external service, needs that service and its name inside the air gap.

## What next

- [Run an air-gapped cluster with the cluster registry](../kubernetes/cluster-registry.md) covers
  the registry's endpoints, its disk, its failures and the `chalkctl registry` commands.
- [Registries and mirrors](../operate/registries.md) covers a mirror of your own inside the air gap,
  which the nodes try after the cluster registry.
- [Networking](../how-it-works/node/networking.md#chrony-keeps-the-clock-with-authenticated-servers)
  explains how a node keeps its clock.
