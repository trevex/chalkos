---
title: "chalkctl registry push"
description: "Push an image to every endpoint of the cluster registry"
---

# chalkctl registry push

```sh
chalkctl registry push SOURCE REFERENCE [flags]
```

Pushes the image of SOURCE to every endpoint of the cluster's ClusterRegistry, under REFERENCE
below the endpoint: docker.io/library/nginx:1.27 goes to &lt;endpoint>/docker.io/library/nginx:1.27,
where containerd looks for it. REFERENCE's host must be one the ClusterRegistry mirrors.
REFERENCE may be written as the kinds write images, repository:tag@sha256:&lt;digest>: the image goes
under the tag, and chalkctl refuses a source of another digest before it reaches an endpoint. A
reference by digest alone pushes the manifest without a tag.

SOURCE is an OCI image layout directory, an OCI archive, a docker archive as docker save writes
it, or an image in a registry that the workstation reaches, read with its docker config. chalkctl
reads a local source through before the first endpoint, so a missing blob fails at once; it
unpacks an OCI archive below $TMPDIR. A docker archive holds one platform's image, whose digest
differs from the one its registry serves, so pods and documents that pin the upstream digest do
not find it: push the upstream image or an OCI layout copied from it instead. An index goes with
every image it names, after them, attestations included, so an index of many platforms takes
several times the space of one image. chalkctl may read an image from a registry on a loopback or
private address over plain HTTP; pin REFERENCE to the image's digest, and chalkctl refuses
anything else it reads.

chalkctl prints the image's digest, checks that each endpoint serves it, reports each endpoint,
and fails unless every one has the image. --connect-to reaches an endpoint through another
address, such as a forwarded port, while it still verifies the endpoint's certificate. The
command needs an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Admin client file to authenticate with instead of the secrets file. |
| `--connect-to`{ #flag-connect-to } | list of string | — | ENDPOINT=HOST:PORT: reach an endpoint at another address, such as a forwarded port, while still verifying its certificate for the endpoint; may be repeated, once per endpoint. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |

## Examples

```sh
chalkctl registry push busybox.tar docker.io/library/busybox:1.37.0 --secrets secrets.age

# The control-plane image a Cluster names, from a registry the workstation reaches.
chalkctl registry push registry.k8s.io/kube-apiserver:v1.37.2 \
  registry.k8s.io/kube-apiserver:v1.37.2@sha256:3e563afb5cb5b72a10bac7012698dd09f5a42bde7d22de8d7d3c12e654df86b4

# Through a forwarded port to the endpoint fd00::11.
chalkctl registry push busybox.tar docker.io/library/busybox:1.37.0 --connect-to fd00::11=127.0.0.1:15000
```

## See also

- [chalkctl registry](chalkctl_registry.md): Manage the images of the cluster registry
