---
title: "Run an air-gapped cluster with the cluster registry"
description: "Serve container images from the control planes to a cluster that cannot reach their registries"
---

# Run an air-gapped cluster with the cluster registry

This page is for an operator whose Kubernetes cluster has no connection to the registries its
container images come from. The cluster registry is a registry on the control planes:
chalkos-registry runs on each node whose address the `ClusterRegistry` document lists, and
containerd on every node looks there first for the images of the hosts the document mirrors. At
the end every node pulls the control plane's images and the workloads' images from the control
planes, and `chalkctl registry list` shows the same images on every endpoint.

A cluster that reaches the registries, or a mirror on its network that holds every image, does not
need the cluster registry; [Registries and mirrors](../operate/registries.md) covers those. The
cluster registry suits a cluster with nothing else to pull from, such as one on an isolated site.

The configuration on this page adds a `ClusterRegistry` to the homelab, whose control planes
`node1`, `node2` and `node3` have the addresses `fdc8:a1b2:c3d4:ff::11` to
`fdc8:a1b2:c3d4:ff::13`. The output comes from a lab cluster whose control plane `cp1` at
192.168.100.11 serves the registry and whose worker `w1` pulls from it. The lab's
`ClusterRegistry` mirrors `cluster-registry.chalkos.test`, a host the lab cannot reach.

## Before you begin

- You have the workstation that [Set up your workstation](../operate/workstation.md) describes,
  with the inventory of a Kubernetes cluster.
- You have the cluster's secrets, from any source `--secrets` takes, as
  [Manage secrets](../operate/secrets.md) describes, or an admin client file. Pushing, deleting
  and collecting garbage need the admin role. Listing needs a client file of any role, because
  chalkctl verifies the endpoints by the OS CA it holds.
- The workstation reaches every endpoint on the registry's port, TCP 5000 by default.
- You have the images to push: as OCI image layouts or archives carried into the air gap, or in a
  registry the workstation reaches.
- The nodes' images carry the cluster registry. Kubernetes images do by default. An image built
  with [`chalkos.kubernetes.registry.enable`](../reference/image-options.md#chalkoskubernetesregistryenable)
  set to false does not, and refuses the `ClusterRegistry` kind.

## Write the ClusterRegistry

One `ClusterRegistry` document turns the cluster registry on. Put it in a file that every
Kubernetes node gets, such as one in `cluster/`:

```yaml title="cluster/cluster-registry.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: ClusterRegistry
metadata: {name: cluster}
spec:
  mirrors: [docker.io, ghcr.io, registry.k8s.io]
  endpoints: ["fdc8:a1b2:c3d4:ff::11", "fdc8:a1b2:c3d4:ff::12", "fdc8:a1b2:c3d4:ff::13"]
  port: 5000
```

`mirrors` lists the registry hosts whose images come from the cluster registry first, as image
references name them and without a port. `docker.io` is Docker Hub. The registry holds each image
below its host's name: `docker.io/library/nginx:1.27` is
`<endpoint>:5000/docker.io/library/nginx:1.27`. containerd mirrors whole hosts, so it looks for
every image of a mirrored host in the cluster registry first. In an air-gapped cluster, `mirrors`
holds every host the `Cluster` document's images come from: `registry.k8s.io` for the control
plane, CoreDNS and the pod sandbox, and `ghcr.io` for flannel.

`endpoints` lists IP addresses, without names or ports. A node serves the cluster registry when one
of its static addresses, the `Address=` values of its `Network` documents, is an endpoint. List an
address of every control plane. The image refuses a control plane whose addresses are missing,
with this message for `chalkctl validate`, `chalkctl config apply` and the node itself:

```text
the control plane <node> serves the cluster registry, but the ClusterRegistry's endpoints name none of its static addresses (<addresses>); add one of them to the endpoints and apply the config to every node
```

`<node>` is the node's name and `<addresses>` its static addresses.

`port` is 5000 by default. It must be 1024 or above, because chalkos-registry runs without
privileges.

Every Kubernetes node holds the same document. chalkctl refuses an inventory whose nodes hold
different `ClusterRegistry` specs, or one on some nodes and none on others, as it does for
`Cluster`. A router image does not know the kind, so the file goes to the Kubernetes nodes alone.
[Config kinds](../reference/kinds.md#clusterregistry) lists the fields.

A serving node runs the registry under a user of its own, keeps its images in
`/var/lib/chalkos-registry` and opens the port in its firewall. containerd on it pulls from its own
endpoint first. Every node then tries the other endpoints in the order of `endpoints`, then the
host's `Mirror` and the host itself.
[Pull from the cluster registry](../operate/registries.md#pull-from-the-cluster-registry) lists the
order, and
[How Kubernetes runs on a node](../how-it-works/kubernetes/overview.md#the-cluster-registry-serves-images-from-the-control-planes)
the mechanism.

## Apply the config to every node

A cluster that is not installed yet gets the document with its install, and each control plane
starts the registry at its first boot. A running cluster gets it with `chalkctl config apply` to
every Kubernetes node:

```sh
chalkctl config apply node1 node2 node3 --secrets=secrets.age
```

The delivery rewrites containerd's mirrors on every node and starts the registry on serving nodes.
containerd reads the mirrors on every pull, so the kubelet keeps running. A kubelet that the same
delivery restarts for another change waits for the node's registry, so it pulls from it at once.

The lab's delivery also added a `Registry` with credentials for `cluster-registry.chalkos.test`.
That `Registry` restarts `chalkos-registry-credentials.service` and `kubelet.service` on its own,
as [the credentials](../how-it-works/kubernetes/overview.md#pods-pull-private-images-with-node-credentials)
need, so the kubelet restarts after the registry. On `cp1` the run printed:

```text
cp1: config 2 added, 0 removed, 0 changed
restarted chalkos-registry-credentials.service
restarted chalkos-registry-mirrors.service
restarted chalkos-registry.service
restarted kubelet.service
cp1 runs config 68029e1e09940d0aecf780223d9af91ca4dd1e8da3072d8054995cca55de0feb
```

A worker serves no endpoint, so a `ClusterRegistry` restarts `chalkos-registry-mirrors.service`
alone there. In the lab, `w1` restarted the `Registry`'s two units as well:

```text
w1: config 2 added, 0 removed, 0 changed
restarted chalkos-registry-credentials.service
restarted chalkos-registry-mirrors.service
restarted kubelet.service
w1 runs config 84a6e74a9aa45837cf76805559c058caf3233c9aca73904d1f0c11a9fb4c2f8d
```

## Bring the images into the air gap

The `images` block of the [`Cluster`](../reference/kinds.md#cluster) document names the images
of the API server, the controller-manager, the scheduler, kube-proxy, etcd, CoreDNS, the pod
sandbox and, on an image with flannel, flannel, each pinned by digest.
[Name the images of the components](configure.md#name-the-images-of-the-components) explains the
references and their digests.

On a machine connected to the registries, copy each image with all its platforms into an OCI image
layout, keeping its digest. An OCI layout or archive holds one image or index for chalkctl, so give
each image a directory of its own. With [crane](https://github.com/google/go-containerregistry/tree/main/cmd/crane)
and yq:

```sh
mkdir -p images/oci
for ref in $(yq 'select(.kind == "Cluster") | .spec.images[]' cluster/cluster.yaml); do
  repo=${ref%%@*}; name=${repo##*/}; name=${name%%:*}
  rm -rf "images/oci/$name"
  crane pull --format=oci "$ref" "images/oci/$name"
done
```

`crane pull --format=oci` writes the index with every platform it names, with the manifests'
bytes and media types as the registry serves them, so the layout serves the digest the `Cluster`
document pins. A pull into a layout that exists adds a second entry to it, which chalkctl refuses,
so the loop removes the directory first. Carry `images/oci/` into the air gap. A tar of a layout,
`tar -cf <name>.tar -C images/oci/<name> .`, is an OCI archive, which chalkctl reads too.

The indexes are larger than the images a node runs. The homelab's eight images take about
1.2 GiB, of which the pause image's index takes about 400 MiB, most of it its Windows images.

!!! note "skopeo keeps no Docker manifest list in an OCI layout"

    registry.k8s.io serves its images as Docker manifest lists. skopeo's `oci:` and
    `oci-archive:` transports write OCI indexes alone, so
    `skopeo copy --all --preserve-digests` stops on such an image:

    ```text
    Manifest list must be converted to type "application/vnd.oci.image.index.v1+json" to be written to destination, but we cannot modify it: "Instructed to preserve digests"
    ```

    Without `--preserve-digests`, skopeo converts the list, which gives it a digest other than the
    one the `Cluster` document pins. skopeo copies an image whose index is an OCI index already,
    such as flannel's, with its digest kept.

## Push the control plane's images before bootstrap

containerd pulls the `Cluster` document's images once the kubelet starts the static pods, at
bootstrap and when a node joins. On a new cluster, install every control plane with the
`ClusterRegistry` in its config, push the images, and then run
[`chalkctl bootstrap`](../reference/cli/chalkctl_bootstrap.md).

Name each image as the `Cluster` document names it, tag and digest together. This loop pushes
every image of `spec.images` from the directories above:

```sh
for ref in $(yq 'select(.kind == "Cluster") | .spec.images[]' cluster/cluster.yaml); do
  repo=${ref%%@*}; name=${repo##*/}; name=${name%%:*}
  chalkctl registry push "images/oci/$name" "$ref" --secrets=secrets.age
done
```

[`chalkctl registry push`](../reference/cli/chalkctl_registry_push.md) pushes each image under its
tag. It refuses a source of another digest before it reaches an endpoint, so a copy that changed
the image never lands in the registry.

Check that every endpoint holds every image of `spec.images`, as
[Check the endpoints](#check-the-endpoints) describes. Then bootstrap the cluster as
[Bootstrap and get a kubeconfig](bootstrap.md) describes.

### Choose the source

The first argument of `chalkctl registry push` is one of these:

- An OCI image layout directory.
- An OCI archive, a tar of a layout. chalkctl unpacks it below `$TMPDIR`.
- A docker archive, as `docker save` writes it. It holds one platform's image, whose digest
  differs from the one its registry serves. Pods and documents that pin the upstream digest do not
  find it, so it does not serve the images of a `Cluster` document.
- An image in a registry the workstation reaches, read with the workstation's docker config.

chalkctl reads a local source through before it pushes to the first endpoint, so a missing blob
fails at once. A digest that a `Cluster` document pins is usually that of an index, which names an
image per platform. The push carries the index with every image it names, attestations included,
so it takes several times the space of one image.

## Push the workloads' images

Name a workload's image as the pod names it, with its registry host:

```sh
chalkctl registry push busybox.tar docker.io/library/busybox:1.37.0 --secrets=secrets.age
```

A pod that runs `busybox:1.37.0` or `docker.io/library/busybox:1.37.0` then pulls it from the
cluster registry. The host must be one of `mirrors`; chalkctl refuses any other, because containerd
never looks for it in the cluster registry. A reference by digest alone pushes the manifest
without a tag.

The push prints the image's digest, then a line for each endpoint. The lab's push of an OCI
archive printed:

```text
pushing cluster-registry.chalkos.test/e2e/busybox:1.37.0 as cluster-registry.chalkos.test/e2e/busybox@sha256:66a6306db78bf2dbf3487f293aa8d6990d8e506fdffab9cc43fe422becf886e4
192.168.100.11: pushed 192.168.100.11:5000/cluster-registry.chalkos.test/e2e/busybox:1.37.0 as sha256:66a6306db78bf2dbf3487f293aa8d6990d8e506fdffab9cc43fe422becf886e4
```

The lab reaches `cp1`'s registry through a forwarded port, with
`--connect-to 192.168.100.11=127.0.0.1:<port>`. The flag reaches an endpoint at another address
and still verifies its certificate for the endpoint.

## Push every image to every endpoint

Nothing copies images between the endpoints. Each `chalkctl registry` command acts on every
endpoint, one after the other, and reports each. A push fails unless every endpoint serves the
image's digest afterwards:

```text
the push failed on <failed> of <total> endpoints of the cluster registry: <endpoints>; nothing copies images between them, so push again once they are reachable
```

Push the image again once the endpoints it names answer. A control plane that was down while you
pushed misses those images until you push them again, and its kubelet pulls them from the next
endpoint meanwhile.

## Check the endpoints

[`chalkctl registry list`](../reference/cli/chalkctl_registry_list.md) lists the tags of every
endpoint and the endpoints that serve each. In the lab:

```console
$ chalkctl registry list
REPOSITORY                                 TAG     DIGEST               ENDPOINTS
cluster-registry.chalkos.test/e2e/busybox  1.37.0  sha256:66a6306db78b  all
```

`all` means every endpoint serves the tag with the same digest. A tag's digest is the one most
endpoints serve. Otherwise the column lists the endpoints that serve it, followed by a mark for
each other endpoint:

| Mark | Means |
| --- | --- |
| `missing on <endpoint>` | The endpoint lacks the tag. |
| `differs on <endpoint> (<digest>)` | The endpoint serves the tag with another digest. |
| `broken on <endpoint> (lacks <n> images of the index)` | The endpoint lacks images the tag's index names, so a pull of those platforms fails there. |

The command exits 1 when it marks any image or cannot list an endpoint. Push the marked images
again to make the endpoints alike. Images pushed by digest alone, without a tag, are not listed.

## Delete images and free space

Nothing deletes images on its own: no scheduled collection, no expiry and no removal of untagged
manifests. The registry's directory grows until you delete images and collect the garbage.

[`chalkctl registry delete`](../reference/cli/chalkctl_registry_delete.md) deletes a tag or a
digest on every endpoint. An endpoint that does not hold it counts as done.

- A tag goes alone. The manifest it named stays, and with it its layers, and chalkctl prints the
  digest and the tags that still name it.
- A digest takes the manifest. chalkctl refuses a digest that tags name unless `--with-tags` is
  given, and then asks before it deletes those tags with it. Without a terminal, as in a script,
  `--yes` answers the question.
- chalkctl always refuses a digest that a tagged index names, because the index would break.
  Delete the index first.

In the lab:

```console
$ chalkctl registry delete cluster-registry.chalkos.test/e2e/busybox@sha256:66a6306db78bf2dbf3487f293aa8d6990d8e506fdffab9cc43fe422becf886e4 --with-tags
chalkctl: registry delete deletes cluster-registry.chalkos.test/e2e/busybox@sha256:66a6306db78bf2dbf3487f293aa8d6990d8e506fdffab9cc43fe422becf886e4 with the tags 1.37.0 that name it on 192.168.100.11.
Go ahead? [y/N] y
192.168.100.11: deleted 192.168.100.11:5000/cluster-registry.chalkos.test/e2e/busybox@sha256:66a6306db78bf2dbf3487f293aa8d6990d8e506fdffab9cc43fe422becf886e4 and its tags 1.37.0
```

The layers stay on disk until
[`chalkctl registry gc`](../reference/cli/chalkctl_registry_gc.md) collects the garbage on each
endpoint, one after the other:

```console
$ chalkctl registry gc
192.168.100.11: freed 2.1 MiB
```

A collection removes the layers and configs that no manifest references, and keeps every manifest,
tagged or not. Deleting a tag alone frees nothing.

While an endpoint collects, it serves pulls and refuses pushes and deletes with
`the registry is collecting garbage; write again once it finished`. A push whose layers went up
before the collection started may fail at its manifest, because the collection removed them. Run
the command when no push is in progress, and push again what failed. An endpoint that collects
already fails a second request. Once begun, a collection runs to its end: Ctrl-C stops chalkctl,
not the collection.

## Give the registry a disk of its own

The registry keeps its images on VAR by default. A [`Volume`](../reference/kinds.md#volume)
mounted at `/var/lib/chalkos-registry` gives it a disk of its own. Put it in the file of each
serving node:

```yaml title="nodes/node1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: registry}
spec:
  disk: {model: "Samsung SSD 870*", type: ssd}
  format: xfs
  mountPoint: /var/lib/chalkos-registry
```

The registry starts after the mount, and systemd makes the mounted file system the registry
user's before it starts. Add the volume before you install the node, so the registry stores on it
from the start. A volume added later with `chalkctl config apply` restarts the registry, which then
stores on the volume. Images pushed before stay hidden on VAR below the mount, so push them again.

The unit requires the mount, as `RequiresMountsFor=` implies `Requires=`. When the volume's disk is
missing or its mount fails, the registry does not start on that node, and containerd moves on to
the next endpoint. [Add storage volumes](../operate/storage-volumes.md) covers disks and formats.

## Add or remove a control plane

Every control plane serves the cluster registry, so a change of the control planes changes
`endpoints`, and every node needs the new document.

To add a control plane:

1. Add its address to `endpoints`. Its config is refused until the list names it.
2. Apply the config to every node already installed.
3. Install the new node, as [Add a control plane](ha-control-plane.md#add-a-control-plane)
   describes. It pulls the control plane's images from the other endpoints while its own
   registry is empty.
4. Push every image again, so the new endpoint holds them, and check with `chalkctl registry list`.

To remove a control plane, take it out of etcd and the inventory as
[Remove a control plane](ha-control-plane.md#remove-a-control-plane) describes. Then take its
address out of `endpoints` and apply the config to every remaining node. Until then, containerd
passes over the missing endpoint to the next source.

## Turn the cluster registry off

Delete the `ClusterRegistry` document and apply the config to every Kubernetes node. Each node
stops the registry, closes its port and drops the endpoints from containerd's mirrors. The images
stay in `/var/lib/chalkos-registry`.

An image author leaves the feature out of an image with
[`chalkos.kubernetes.registry.enable`](../reference/image-options.md#chalkoskubernetesregistryenable)
set to false. Such an image carries no chalkos-registry, no user `chalkos-registry`, no
`ClusterRegistry` kind and none of its outputs, effects, firewall rules and diagnostics, and it
refuses a config that holds a `ClusterRegistry`.

## Plan around the limits

- Nothing copies images between the endpoints. Each push and delete goes to every endpoint, and
  `chalkctl registry list` shows where they differ.
- containerd mirrors per host. Every image of a mirrored host is looked for in the cluster registry
  first; a host cannot be mirrored for some repositories alone.
- A push that spans a garbage collection may fail and needs to run again.
- `chalkctl registry list` does not show manifests without a tag.
- An endpoint is a node's static address, which its node certificate names, so a VIP cannot be
  one. `chalkctl validate` does not notice an
  endpoint that no node owns; `chalkctl registry push` fails on it, and containerd passes over it.
- chalkd does not pull OS images from the cluster registry. Air-gapped upgrades take the images
  from a directory, as [Upgrade nodes](../operate/upgrade.md) describes.

## Check that it worked

`chalkctl registry list` shows `all` for every image, and exits 0. The diagnostic
`cluster-registry` shows the repositories one node's registry holds, as its catalog lists them:

```console
$ chalkctl node diag cp1 cluster-registry
{"repositories":["cluster-registry.chalkos.test/e2e/busybox"]}
```

The diagnostic reads the registry over its Unix socket on the node, which only the registry's
group reaches, so it works on any port. A pod that runs a pushed image on a node that has no
route to the image's host shows that the node pulled it from the cluster registry.

## If something goes wrong

- The registry does not run on a node that should serve it. Read its journal with
  `chalkctl logs <node> --unit chalkos-registry.service`. When the last line of the unit is
  `Serve the cluster registry skipped, unmet condition check ConditionPathExists=/run/chalkos/outputs/cluster-registry/registry.env`,
  the node does not serve an endpoint: none of its static addresses is in `endpoints`. A
  delivery whose restart of `chalkos-registry.service` fails names the unit. A volume mounted at
  `/var/lib/chalkos-registry` that is missing keeps the registry from starting, as
  [Give the registry a disk of its own](#give-the-registry-a-disk-of-its-own) describes.
- The workstation or the nodes cannot reach the port. `chalkctl node diag <node> nft-ruleset`
  shows the table `inet chalkos-registry`, which opens it. A network firewall in front of the
  nodes must let TCP to the port through, as
  [Ports and firewall](../reference/ports-and-firewall.md) lists.
- `registry push needs the admin role; the reader role may not`: chalkctl authenticated with a
  reader client file. Use an admin client file or the secrets.
- `reading the registry needs a certificate of the OS CA, a node's or a client file's`: a client
  without such a certificate, such as `curl` or a container tool of the workstation, read the
  registry. Read it with `chalkctl registry list` or the diagnostic `cluster-registry` instead.
- `the ClusterRegistry mirrors <hosts>, not <host>, so containerd never pulls <reference> from it; add <host> to its mirrors`:
  the reference's host is not mirrored. Add it to `mirrors` and apply the config to every node.
- `<reference> names the digest <digest>, but the source's image is <other>; push a source of that digest, such as the image in its registry by that reference, since docker save and rebuilt images get other digests`:
  the source is not the image the reference pins, often a docker archive. Push the upstream image
  or an OCI layout copied from it.
- Pods stay in `ErrImagePull` or `ImagePullBackOff`. Check with `chalkctl registry list` that the
  endpoints hold the image under the reference the pod names, then read
  `chalkctl logs <node> --unit containerd.service` for the URLs containerd tried.
  [Troubleshooting](../operate/troubleshooting.md#image-pulls-fail) covers the mirrors' units.

## What next

- [Registries and mirrors](../operate/registries.md) combines the cluster registry with `Mirror`
  and `Registry` documents.
- [How Kubernetes runs on a node](../how-it-works/kubernetes/overview.md#the-cluster-registry-serves-images-from-the-control-planes)
  explains the registry's TLS, roles and garbage collection.
- [`chalkctl registry`](../reference/cli/chalkctl_registry.md) lists the commands and their flags.
