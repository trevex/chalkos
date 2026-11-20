---
title: "Recurring tasks"
description: "What expires when, how to check it, what to back up, and how to move a cluster to a newer chalkos"
---

# Recurring tasks

A cluster's certificates and CAs expire, and a few of its files exist only where you keep them.
This page lists what to do and when, and how to move the cluster to a newer chalkos. It links to
the task pages for each step.

In short:

```sh
chalkctl status <node>          # the certificates: section shows what expires when
chalkctl client new --name=<name> --role=<role> --secrets secrets.age --force
chalkctl rotate node-ca --secrets secrets.age
nix flake update chalkos        # after changing the input's tag; then rebuild, validate and upgrade
```

## Before you begin

You need the cluster's secrets file and a workstation set up as
[Set up your workstation](workstation.md) describes.

## Check what expires

`chalkctl status <node>` lists each certificate the node holds with its expiry, and adds a note
when something needs doing, such as `node CA expires <date>; run chalkctl rotate node-ca`.
chalkctl itself warns about a client file in its last 30 days.

| What | Lifetime | Renewed by | What you do |
| --- | --- | --- | --- |
| Client file | 1 year, or `--ttl` | nobody | Issue a new one before it expires. |
| Admin kubeconfig | 1 year, or `--ttl` | nobody | Write a new one with `chalkctl kubeconfig`. |
| Node certificate | 1 year | chalkd, on a node with Kubernetes | Run `chalkctl node renew <node>` on a node without Kubernetes. |
| Control plane and kubelet certificates | 1 year | the control plane and the kubelet | Nothing, unless status adds a note. |
| Node CA | 5 years | nobody | Rotate it in its last 18 months. |
| OS CA | 10 years | nobody | Rotate it in its last year. |
| Kubernetes, front-proxy and etcd CAs | 10 years | nobody | Rotate them in their last year. |
| Secure Boot certificates | 20 years | nobody | Nothing: firmware does not check their validity. |

[Certificates](../reference/certificates.md) lists every certificate, its renewal point and the
notes status shows.

## Renew what does not renew itself

- Issue a new client file with
  [Issue a client file per person](access.md#issue-a-client-file-per-person). `--force` replaces
  an existing file.
- Renew the node certificate of a node without Kubernetes with
  [Renew node certificates](access.md#renew-node-certificates), before it expires.
- Rotate the node CA with
  [Rotate the node CA before it expires](access.md#rotate-the-node-ca-before-it-expires), and the
  OS CA with [Rotate the OS CA](access.md#rotate-the-os-ca).
- Rotate the Kubernetes, front-proxy and etcd CAs with
  [Rotate the Kubernetes CAs and keys](../kubernetes/rotate-kubernetes-cas.md).

Put the dates in a calendar when you install the cluster. A rotation runs in phases that every
node confirms, so plan it for a time when every node is up.

## Back up what you cannot rebuild

A node can be reinstalled and an image built again, but some files exist only where you keep them.
Back up these files to a place apart from the workstation:

- The secrets file and the age or SSH identities of its recipients, the offline backup key
  included. Without an identity the file cannot be decrypted. A rotation changes the secrets
  file, so back it up again after each one.
- The directory of the Secure Boot keys, PK, KEK and db, which
  [Sign images for Secure Boot](../build/sign-images.md) creates. Every machine's firmware trusts
  their certificates, so new keys mean enrolling new certificates on each machine.
- The repository: the inventory, the config files, `secrets.pub.json` and `flake.lock`, which pins
  the chalkos revision every image is built from.
- The image directory of the image the nodes ran before an upgrade, until the new image has
  proven itself. For an image the nodes pull from a registry, keep its digest in the registry
  instead; the inventory's earlier commit names it. `chalkctl upgrade --rollback` boots the
  previous image from the nodes' inactive slots until the next upgrade writes over them. After
  that, an upgrade to the kept image is the way back, as
  [Go back to the previous image](upgrade.md#go-back-to-the-previous-image) describes.
- The password of each node whose encryption fallback is a password. Nothing else holds it.

With the Kubernetes layer, the cluster's state lives in etcd on the control planes. chalkos has no
etcd backup or restore yet; the [roadmap](../contribute/roadmap.md#smaller-items) lists it. Three
control planes survive the loss of a machine, but they do not bring back an object that someone
deleted. Back up what the workloads keep with a tool that works at the Kubernetes level.

## Upgrade chalkos itself

The image flake names a chalkos release in its `chalkos` input, such as
`github:trevex/chalkos/v0.1.0`, and `flake.lock` pins its commit. A newer chalkos reaches the
nodes as new images:

1. In `flake.nix`, change the input's tag to the new release, which
   [Releases](../get-started/releases.md) lists, then update the pin in the image flake's
   directory:

    ```sh
    nix flake update chalkos
    ```

2. Read the changelog of every release between the two, on the
   [releases page](https://github.com/trevex/chalkos/releases). Before 1.0 a release can change a
   kind without a migration of its documents.
3. Enter the flake's development shell again, so chalkctl comes from the new release:

    ```sh
    nix develop
    ```

4. Build each image again into the directory the inventory names, and check every node's
   config against them:

    ```sh
    nix build .#<image> -o images/<image>
    chalkctl validate
    ```

    `<image>` is an image's name, such as `node-metal`.

    Fix each document `validate` refuses before you go on.

5. Commit `flake.lock` with the changed config, and roll the images out as
   [Upgrade nodes](upgrade.md) describes, one node first.

## Check that it worked

`chalkctl status <node>` shows no note about an expiring certificate or CA, and each node runs
the image the inventory names for it.

## If it fails

- `the certificate of <path> expired on <date>; issue a new one with chalkctl client new`: the
  client file expired. Issue a new one with the secrets file.
- A node's own certificate expired: `chalkctl node renew <node>` issues a new one, also after
  expiry. [Renew node certificates](access.md#renew-node-certificates) explains the risk.
- `chalkctl validate` refuses documents after an update: a kind changed. The refusal names the
  document and field; change the YAML to the new schema in
  [Config kinds](../reference/kinds.md).
