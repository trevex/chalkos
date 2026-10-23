---
title: "Bootstrap a cluster"
description: "Bootstrap the first control plane of a cluster and issue kubeconfigs for its Kubernetes API"
---

# Bootstrap a cluster

Once its nodes are installed, a cluster starts on one control plane, and kubeconfigs give people its
Kubernetes API.

## Bootstrap the first control plane

A cluster starts once, on one control plane. When `cp1` is back up in normal mode, run
[`chalkctl bootstrap`](../reference/cli/chalkctl_bootstrap.md):

```sh
chalkctl bootstrap cp1 --secrets=secrets.age
```

The node starts etcd as its first member, starts the API server, controller-manager and
scheduler, and applies chalkos's addons and the objects of the cluster's `Addon` documents.
chalkctl prints `bootstrapping cp1; the control plane pulls its images and starts`, waits, and
ends with `cp1 is bootstrapped; applied <n> objects`. The node refuses a second bootstrap, so a
cluster is never initialised twice.

## Get a kubeconfig

[`chalkctl kubeconfig`](../reference/cli/chalkctl_kubeconfig.md) writes a kubeconfig with a
cluster-admin certificate, valid for a year, to `./kubeconfig`:

```sh
chalkctl kubeconfig --secrets=secrets.age
kubectl --kubeconfig kubeconfig get nodes
```

## Give someone access to the Kubernetes API

[`chalkctl kubeconfig`](../reference/cli/chalkctl_kubeconfig.md) writes a kubeconfig whose client
certificate puts its holder in `chalkos:cluster-admins`, which is bound to `cluster-admin`:

```console
$ chalkctl kubeconfig --name=alice --ttl=2160h --out=alice.kubeconfig --secrets=secrets.age
wrote alice.kubeconfig; its certificate is valid for 2160h0m0s
```

`--name` becomes the Kubernetes user name, which authorisation and audit logs see. Every
kubeconfig chalkctl issues grants full access; narrower roles need an authenticator you add to
the API server.

Nothing revokes a single kubeconfig. It works until its certificate expires or the Kubernetes CA
is rotated, so a short `--ttl` makes it lapse without a rotation.
[Revoke Kubernetes access](rotate-kubernetes-cas.md#revoke-kubernetes-access) covers the rotation.
