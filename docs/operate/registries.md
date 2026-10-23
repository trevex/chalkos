---
title: "Registries and mirrors"
description: "Give nodes the credentials of a private registry, and pull images through a mirror"
---

# Registries and mirrors

This page is for an operator whose inventory names an image in a registry, as
[Publish images to a registry](../build/publish-images.md) pushes one. At the end each node holds
the credentials of a private registry in its config, and pulls through a mirror before it asks the
registry itself. The homelab's nodes pull from `ghcr.io` through a mirror on the local network.

A node pulls its own image at an install and at an upgrade. chalkctl sends it the image's
reference by digest, and chalkd pulls the image with the `Registry` and `Mirror` documents of its
config.

## Before you begin

You need the workstation that [Set up your workstation](workstation.md) describes, with the secrets
file `secrets.age`, and an inventory that names an image by `source.oci`.

chalkctl reads the image's manifest, header and contract on your workstation, with your docker
config. The workstation needs a connection to the registry itself, because chalkctl uses no
`Mirror`. Each node needs a connection to the registry or to a mirror's endpoint.

## Give the nodes the registry's credentials

A `Registry` document gives the credentials for one registry host. Its password comes from a
`Secret`:

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

`<token>` stands for the registry's access token. Put both documents in a file that every node
gets, such as the homelab's `cluster/` directory.

The config holds one `Registry` per host. `host` is the registry's host as the image reference
names it. chalkd matches it in any case and as image references name registries, so `docker.io`
matches `index.docker.io`. A `Registry` gives `username` and `password` together, or neither for
anonymous pulls. A public registry that serves the image anonymously needs no `Registry`.

chalkd reads the credentials from one config:

- At an upgrade, from the config the upgrade delivers. An upgrade without config, such as one
  with an operator client file, uses the config the node runs.
- At an install, from the config the install delivers, because the installer has no config of
  its own.

## Allow a registry that serves plain HTTP

chalkd pulls over HTTPS and trusts the certificate authorities of the image's system trust store.
A `Registry` adds no CA of its own. A registry that serves only plain HTTP, such as one in a lab,
needs `insecure: true`:

```yaml title="cluster/registry.yaml"
apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: lab}
spec:
  host: registry.lab:5000
  insecure: true
```

`insecure` is needed also when the registry has a private or loopback address. With it, the
credentials and the image travel unencrypted, so anyone on the network path can read the
credentials. The image stays protected, because the node checks every byte against the digests
chalkctl read and against the root hash. Set `insecure` only for a registry on a trusted network.

## Pull through a mirror

A `Mirror` document puts endpoints in front of one registry host:

```yaml title="cluster/registry.yaml"
apiVersion: oci.chalkos.dev/v1alpha1
kind: Mirror
metadata: {name: ghcr}
spec:
  registry: ghcr.io
  endpoints: [https://mirror.lan/ghcr]
```

chalkd tries each endpoint in order, then the registry. On an endpoint it asks for the same
repository and digest below the endpoint's path, as containerd does. The image
`ghcr.io/example/node-metal` comes from `https://mirror.lan/ghcr/v2/example/node-metal/...`. An
endpoint whose path ends in `/v2` serves the API at that path.

Each endpoint authenticates with the `Registry` of its own host, here `mirror.lan`, or
anonymously without one. The credentials of `ghcr.io` go to `ghcr.io` alone. An `http://`
endpoint needs a `Registry` of its host with `insecure: true`, and chalkctl and chalkd refuse a
config without it.

chalkd reads the `Mirror` documents from the same config as the `Registry` documents.

## A failing endpoint passes the pull on

chalkd passes over an endpoint and asks the next source when the endpoint:

- cannot be reached, refuses the connection or fails the TLS handshake;
- gives no manifest and header within 30 seconds;
- answers 401, 403, 404, 429 or a 5xx status, as a pull-through cache can when it lacks a layer;
- sends no data for two minutes after a layer starts.

The manifest comes from the first source that serves it. Each layer starts at that source and
moves on under the same rules. A source that serves bytes other than those of the pinned digest,
or a header or layer sizes other than those chalkctl sent, fails the pull at once, without asking
the next source.

When the last source fails too, the error names the source it ended at, followed by
`after the mirror <endpoint>: <error>` for each endpoint it passed over.

## Container images use the same documents

On the shipped variant, containerd pulls the pods' container images through the endpoints of the
same `Mirror` documents. The same `Registry` gives every pod on the node the images of its host,
without `imagePullSecrets`.
[How do pods pull images from a private registry?](../how-it-works/kubernetes/kubernetes.md#how-do-pods-pull-images-from-a-private-registry)
describes how the kubelet gets the credentials.

!!! warning "On the shipped variant, a mirror receives the registry's credentials"

    The kubelet gives containerd the credentials for the image's registry without the host they
    belong to. containerd sends them to every endpoint that asks for credentials, and to the token
    service such an endpoint names. A mirror of an authenticated registry therefore receives that
    registry's credentials on container pulls. Give its endpoints the trust you give the registry.

## Check that it worked

`chalkctl validate` checks the `Registry` and `Mirror` documents with the rest of each node's
config:

```sh
chalkctl validate
```

Then upgrade one node, as [Upgrade nodes](upgrade.md) describes:

```sh
chalkctl upgrade --nodes=w1 --secrets secrets.age
```

The node pulls the image with the delivered config. A run that ends with
`upgraded 1 node to <new>` pulled it. `<new>` stands for the image's name.

## If something goes wrong

- `the mirror <endpoint> is plain HTTP; set insecure: true in the Registry of <host> to allow it`:
  add a `Registry` of the endpoint's host with `insecure: true`.
- `plain HTTP to <host> is refused: set insecure: true in the Registry of the host to allow it`:
  the registry or an endpoint redirected to plain HTTP.
- The registry refuses the pull with 401 or 403: compare the `Registry`'s `host` with the host as the
  image reference names it, port included.
- On the shipped variant, pods stay in `ErrImagePull` or `ImagePullBackOff`.
  [Troubleshooting](troubleshooting.md#image-pulls-fail) reads the units that write the
  credentials and the mirrors.

## What next

- [Publish images to a registry](../build/publish-images.md) pushes the image and names it in the
  inventory.
- [Ports and firewall](../reference/ports-and-firewall.md#connections-a-node-makes) lists the
  connections a node makes.
- [Config kinds](../reference/kinds.md#registry) lists every field of `Registry` and `Mirror`.
