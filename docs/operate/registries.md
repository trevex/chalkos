---
title: "Registries and mirrors"
description: "Give nodes the credentials of a private registry, and pull images through a mirror"
---

# Registries and mirrors

These sections give nodes the credentials of a registry that the inventory names an image in, as
[Publish images to a registry](../build/publish-images.md) pushes it, and route their pulls through
a mirror.

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
