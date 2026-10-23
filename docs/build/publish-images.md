---
title: "Publish images to a registry"
description: "Build an image as an OCI artifact, sign it for Secure Boot, push it and name it in the inventory"
---

# Publish images to a registry

A chalkos image can be published to an OCI registry as one artifact, so a cluster's inventory names
it by its reference in the registry. The artifact holds the same
image as an image directory: the store, its hash tree,
the UKI, the boot loader and the contract, which describes the
config the image accepts. [`chalkctl validate`](../reference/cli/chalkctl_validate.md),
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) and
[`chalkctl status`](../reference/cli/chalkctl_status.md) read the image's header and contract from
the registry. [`chalkctl install`](../reference/cli/chalkctl_install.md) on a machine that runs
the installer and [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) both have the node
pull the image from it. This guide builds the artifact with `mkImages`, signs it, pushes it with
chalkbuild, names it in the inventory, gives the nodes the
registry's credentials, and installs and upgrades them from it.

## Before you begin

You need:

- chalkbuild, from `nix shell github:trevex/chalkos#chalkbuild` or from
  `chalkos.packages.${system}.chalkbuild` in the cluster flake's dev shell.
- Push access to a registry, with credentials that `docker login` stored in docker's config or a
  credential helper. chalkbuild reads them from there.
- For machines with Secure Boot, the db key and certificate of
  [Sign images for Secure Boot](secure-boot-signing.md).

## Expose the layout

`mkImages` returns, beside each image directory `images.<variant>-<platform>`, its OCI image layout
as `oci.<variant>-<platform>`. An OCI image layout is the directory form of an OCI image that
registries and tools such as skopeo copy. Each layout is built only when asked for. Expose the one
to publish as a package of the cluster's flake, where `images` is the result of `mkImages`:

```nix
packages.x86_64-linux.node-metal-oci = images.oci.node-metal;
```

## Build and sign the layout

Build the layout, then sign it into a directory of its own with the db key:

```sh
nix build .#node-metal-oci -o build/node-metal-oci
rm -rf oci/node-metal
chalkbuild sign --key=db.key --cert=db.crt build/node-metal-oci oci/node-metal
```

[`chalkbuild sign`](../reference/cli/chalkbuild_sign.md) signs copies of the UKI and boot-loader
layers, checks them against `--cert` and writes a new layout with them and a new manifest. The
store, hash tree and contract layers are copied as they are. The output directory must not exist,
so the previous signed layout is removed first.

Machines without Secure Boot need no signature: skip `chalkbuild sign` and push
`build/node-metal-oci` itself.

## Push the layout

[`chalkbuild push`](../reference/cli/chalkbuild_push.md) pushes the layout to a reference that
names the registry and a tag or digest, and prints the reference of the pushed manifest by digest:

```sh
chalkbuild push oci/node-metal ghcr.io/example/node-metal:v1.5.0
```

It prints `ghcr.io/example/node-metal@sha256:<digest>`. chalkbuild reaches a registry at a
loopback or private (RFC 1918) address over plain HTTP, and any other registry over HTTPS. Some
registries, Quay among them, reject a manifest of an artifact type they do not recognize until an
operator allow-lists `application/vnd.chalkos.image.v1`. The layout is a standard OCI image layout,
so any tool that copies one, such as skopeo or oras, can push it too.

## Name the image in the inventory

The image's `source` in the inventory's `spec.images` takes the reference as `oci`, in place of
`directory`:

```yaml title="chalkos.yaml"
spec:
  images:
    node-metal: {source: {oci: ghcr.io/example/node-metal@sha256:<digest>}}
```

A reference by digest pins the image. A reference by tag is resolved to a digest once per chalkctl
command, so one command reads one image even when the tag moves while it runs.
[How does the inventory name images?](../how-it-works/node/config.md#how-does-the-inventory-name-images)
covers the rest of `spec.images`.

## What does the artifact hold?

The artifact is one manifest of the artifact type `application/vnd.chalkos.image.v1`. Its config is
the image's header and its layers are the image's parts, uncompressed. [Image and partition
layout](../reference/image-layout.md#oci-artifact) lists the media types and the header's fields.

## Check that it worked

`chalkctl validate` reads the header and contract from the registry and checks each node's config
against them:

```sh
chalkctl validate
```

It prints a line such as `cp1: valid for the image node-metal at
ghcr.io/example/node-metal@sha256:<digest> (node on metal, root hash 2ece03b0fa09), config <hash>`
for each node whose config is valid. The digest is that of the manifest chalkctl read, so it names
the image that was checked even when the inventory names a tag.

## Give the nodes the registry's credentials

A node authenticates its pull with a [`Registry`](../reference/kinds.md#registry) document of its
config, whose `host` is the registry's host as the image reference names it. A public registry
that serves the image anonymously needs no `Registry`. The password comes from a `Secret`, so add
both documents to a file every node gets:

```yaml title="cluster/registry.yaml"
apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: ghcr}
spec:
  host: ghcr.io
  username: example-bot
  password: {secretRef: {name: registry, key: token}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: registry}
spec:
  data: {token: <token>}
```

The config holds one `Registry` per host. A `Registry` gives `username` and `password` together,
or neither to pull from the host anonymously. chalkd matches the host in any case and as image
references name registries, so `docker.io` matches `index.docker.io`. For an upgrade, it takes
the `Registry` documents of the config the upgrade delivers, or, when the upgrade carries no
config, those of the config the node runs. For an install, it takes only those of the config the
install delivers, because the installer has no config of its own. Keep the file in a list every
node gets, so installs and upgrades both find the credentials.

On an image with Kubernetes, the same `Registry` also gives every pod on the node the container
images of its host, without `imagePullSecrets`.
[How do pods pull images from a private registry?](../how-it-works/kubernetes/kubernetes.md#how-do-pods-pull-images-from-a-private-registry)
describes how the kubelet gets the credentials and why every pod on the node can use them.

chalkd pulls over HTTPS only and trusts the certificate authorities of the image's system trust
store; a `Registry` adds no CA of its own. A registry that serves only plain HTTP, such as one in
a lab, needs `insecure: true` in its `Registry`, also when the registry has a private or loopback
address. With it, the credentials and the image travel unencrypted, so anyone on the network path
can read the credentials. The image stays protected, because the node checks every byte against
the digests chalkctl read and against the root hash. Set `insecure` only for a registry on a
trusted network, such as a lab.

## Pull through a mirror

A [`Mirror`](../reference/kinds.md#mirror) document puts endpoints in front of one registry host.
chalkd pulls an install's or an upgrade's image through them, and containerd pulls container
images through them:

```yaml title="cluster/registry.yaml"
apiVersion: oci.chalkos.dev/v1alpha1
kind: Mirror
metadata: {name: ghcr}
spec:
  registry: ghcr.io
  endpoints: [https://mirror.lan/ghcr]
```

chalkd tries each endpoint in order, then the registry itself. On an endpoint it asks for the
same repository and digest, below the endpoint's path, as containerd does: the image
`ghcr.io/example/node-metal` comes from `https://mirror.lan/ghcr/v2/example/node-metal/…`. An
endpoint whose path already ends in `/v2` serves the API at that path.

Each endpoint authenticates with the `Registry` of its own host, here `mirror.lan`, or
anonymously without one; the credentials of `ghcr.io` go to `ghcr.io` alone. An `http://`
endpoint needs a `Registry` of its host with `insecure: true`, and chalkctl and chalkd refuse a
config without it. An `https://` endpoint is reached over HTTPS alone.

chalkd passes over an endpoint and asks the next source when the endpoint:

- cannot be reached, refuses the connection or fails the TLS handshake;
- gives no manifest and header within 30 seconds;
- answers 401, 403, 404, 429 or a 5xx status, for example because it lacks the header, the
  contract or a layer, as a pull-through cache can;
- sends no data for two minutes as a layer starts.

The manifest comes from the first source that serves it, and each layer starts at that source and
moves on under the same rules. A source that serves bytes other than those of the pinned digest,
or a header or layer sizes other than those chalkctl sent, fails the pull at once, without asking
the next source. When the last source fails too, the error ends with the endpoints passed over and
why.

chalkd takes the `Mirror` documents from the same config as the `Registry` documents: for an
upgrade, the config the upgrade delivers, or else the config the node runs; for an install, the
config the install delivers. chalkctl does not use a node's `Mirror`. It reads the image's
manifest, header and contract on the operator's machine, from the inventory's reference and with
the operator's docker config, so that machine needs a connection to the registry itself.

containerd authenticates endpoints differently. It sends every endpoint that asks for credentials,
and the token service such an endpoint names, the credentials the kubelet gives it for the image's
registry, because the kubelet passes them without the host they belong to. A mirror of an
authenticated registry therefore receives that registry's credentials on container pulls, so give
its endpoints the trust the registry has, as
[How do pods pull images from a private registry?](../how-it-works/kubernetes/kubernetes.md#how-do-pods-pull-images-from-a-private-registry)
describes.

## Install from the registry

`chalkctl install` without `--image`, on a machine that runs the
installer, sends the installer the image's reference by
digest in place of the image:

```sh
chalkctl install cp1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
```

It prints a line such as `installing cp1 onto serial "S7KHNJ0W100001": chalkos 81bd02c4a9e1
(0f1e2d3c4b5a), which the installer pulls from ghcr.io/example/node-metal@sha256:<digest>`. The
installer pulls the store, the hash tree, the UKI and the boot loader, and takes the image's
schemas, `image.json` and partition definitions from the artifact's contract layer. The machine
that runs the installer therefore needs a route and DNS to the registry or to an endpoint of its
`Mirror`, as
[Ports and firewall](../reference/ports-and-firewall.md#connections-a-node-makes) lists.

The installer checks the pulled layers as it checks a streamed image: their digests, the store
against the root hash, and the UKI and boot loader against the firmware's db and dbx when Secure
Boot is enforced. It reads the manifest and the contract before it touches the disk, so when no
source serves them, because each is unreachable, refuses the credentials or lacks the digest, the
install fails with an error that names the reference, and the disk stays as it was. An install
that stops partway continues when the command runs again, by a pull or with `--image`.

## Upgrade from the registry

`chalkctl upgrade` resolves the inventory's reference to a digest once and sends each node the
reference by that digest, so every node pulls the same image even when the tag moves during the
run:

```sh
chalkctl upgrade
```

chalkctl reads only the manifest, the header and the contract from the registry; each node pulls
the store, the hash tree and the UKI itself, so a node needs a connection to the registry or to
an endpoint of its `Mirror`, as
[Ports and firewall](../reference/ports-and-firewall.md#connections-a-node-makes) lists. chalkd
checks the pulled header and layer digests against the ones chalkctl sent, then checks the layers
as it checks a streamed image: their SHA-256 sums, the store against the root hash, and the UKI
against the firmware's db and dbx when Secure Boot is enforced. A layer whose source stops sending
data for two minutes after it started stops that node's upgrade. A node that has the image staged
already gets it without contacting the registry.

## What next

- [Install on bare metal](../operate/install-bare-metal.md) boots the installer and installs a machine from
  it.
- [Sign images for Secure Boot](secure-boot-signing.md) creates the db key and gets the firmware
  to trust it.
- [`chalkbuild oci`](../reference/cli/chalkbuild_oci.md) writes a layout from an image directory
  outside Nix.
- [Upgrade a cluster](../operate/upgrade-cluster.md) rolls an image through the nodes, from a registry or a
  directory.
- [Upgrades](../how-it-works/node/upgrades.md) explains what each node checks before the image boots.
