---
title: "Plan a production cluster"
description: "Decide the variants, addresses, storage, keys and time sources of a production cluster before installing it"
---

# Plan a production cluster

A production cluster needs decisions that the [quick start](../get-started/quick-start.md)'s lab
makes for you. A few of them cannot change once the cluster runs: the secrets file and the Secure Boot signing
key. [Plan a cluster](plan.md) takes the Kubernetes decisions. This page lays out the
cluster's repository, then takes the decisions in the order you meet them while writing its
files, each with a recommendation and what it costs.

## Before you begin

- Run the [quick start](../get-started/quick-start.md) once, so the commands and the status output
  are familiar.
- Know the machines: their disks, network segments and whether their firmware lets you enrol
  your own Secure Boot keys.
- Read [Your first image flake](../build/first-image.md) for how the flake builds images, and
  [Config and the inventory](../how-it-works/node/config.md) for how the YAML files become each node's
  config.

## Lay out the repository

A cluster lives in one repository with two halves. The flake builds the images with
`chalkos.lib.mkImages`; the inventory, `chalkos.yaml`,
names the nodes and the YAML files each one gets. chalkctl
reads the inventory and the YAML and never runs Nix, so the machine that installs and upgrades
nodes needs the signed image directories, not Nix.

```text
prod/
├── flake.nix            mkImages: the images of the cluster
├── flake.lock           the chalkos version every image is built from
├── secrets.pub.json     the public half of the secrets file, which the images carry
├── chalkos.yaml         the inventory
├── cluster/             files every node gets
│   ├── kubernetes.yaml
│   ├── registries.yaml
│   └── time.yaml
├── groups/storage.yaml  files the nodes of the storage group get
├── nodes/               each node's own file
│   ├── cp1.yaml
│   ├── cp2.yaml
│   ├── cp3.yaml
│   └── w1.yaml
├── build/               image directories nix build links, outside git
├── images/              signed copies of them, which the inventory names, outside git
├── installer/           the installer nix build links, outside git
└── installer-signed/    its signed copy, outside git
```

The flake builds one image per platform of the variant `node`, which serves control planes and
workers alike, and exposes the images and the installer
as packages:

```nix title="flake.nix"
{
  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { self, chalkos, ... }:
    let
      images = chalkos.lib.mkImages {
        osCA = ./secrets.pub.json;
        # The commit the images are built from, shown beside their root hashes.
        revision = self.rev or self.dirtyRev or null;
        platforms = [ "metal" ];
      };
    in
    {
      # nix build .#node-metal -o images/node-metal, nix build .#installer -o installer
      packages.x86_64-linux = images.images // {
        inherit (images) installer;
      };
    };
}
```

The inventory gives every node the files of `cluster/` through `spec.all`, the nodes of a group
the group's files, and each node its own file, in that order. Each node's `address` is where
chalkctl reaches it. `spec.images` names the image directories, relative to the inventory's
directory, here `images/node-metal-signed`, where `chalkbuild sign` writes the signed copy of
the build in `images/node-metal`. `spec.all.imageName`
names the image every node runs unless the node or one of its groups names another:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: prod}
spec:
  images:
    # chalkbuild sign signs a copy of the build into this directory.
    node-metal: {source: {directory: images/node-metal-signed}}
  all: {imageName: node-metal, files: [cluster/*.yaml]}
  groups:
    storage: {files: [groups/storage.yaml]}
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    cp2: {address: 10.0.0.12, files: [nodes/cp2.yaml]}
    cp3: {address: 10.0.0.13, files: [nodes/cp3.yaml]}
    w1: {address: 10.0.0.21, groups: [storage], files: [nodes/w1.yaml]}
```

The storage group makes its nodes workers with the labels and taints of storage nodes. A set
holds one `Node` document, so the files of a node in the group hold no other:

```yaml title="groups/storage.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: worker
  labels: {node.kubernetes.io/storage: ssd}
  taints: [{key: dedicated, value: storage, effect: NoSchedule}]
```

The inventory's `metadata.name` names the cluster in chalkctl's messages and in the kubeconfig
`chalkctl kubeconfig` writes; it is a label and reaches no node. The keys of `spec.nodes` are the
nodes' names, which last: each node's certificate, Kubernetes node, etcd member and recovery key
carry its name, so renaming a node takes a reinstall, as
[What names a node?](../how-it-works/node/config.md#what-names-a-node) describes.

A node's set holds at most one document of most kinds, and exactly one `SystemDisk`, `Cluster`
and `Node`, so each document lives in the one place that fits it: what every node shares in
`cluster/`, what a kind of node shares in a group, the rest in the node's file.
[The kinds reference](../reference/kinds.md) lists every field.

## Choose the variants

Use one variant, `node`, for control planes and workers. Each node's `Node` document makes it a
control plane or a
worker, so one image per platform serves the whole cluster
and an upgrade builds and signs one image.

Add a variant only for nodes that need other software in their
image, such as GPU drivers or an extension's service, and name every variant the flake builds:
`variants` replaces the default `node` variant, so `variants.gpu.modules` alone builds no `node`
images. With `node = { };` beside `gpu`, the flake builds `node-<platform>` and `gpu-<platform>`
images, and the nodes that run the GPU images name theirs in the inventory, on the node or on a
group: a node's own `imageName` wins over its groups', which win over `spec.all.imageName`, and a
node in two groups that name different images is refused.
Every variant is built for every platform the flake names, so name only the platforms you run.
[Variants and platforms](../build/variants-and-platforms.md) adds modules to a variant.

## Plan the Kubernetes side

The control planes, the cluster endpoint, the control planes' addresses, the IP families and
scheduling on control planes are on [Plan a cluster](plan.md). The `Flannel` document's VXLAN
sources and the other Kubernetes settings are on [Configure Kubernetes](configure.md).

## Plan disks, storage and encryption

Name the system disk in the node's [`SystemDisk`](../reference/kinds.md#systemdisk) document by a
path that survives a reordering of the disks, such as `/dev/disk/by-id/...`, not `/dev/sda`.
VAR, which holds etcd, containerd's images and the logs, fills the
rest of the system disk unless `spec.var.size` limits it. Volumes for local persistent volumes or
a storage system are [`Volume`](../reference/kinds.md#volume) documents on the system disk or on
disks of their own, picked by path or by a selector of model, serial number, WWN, size and type.
[Add storage volumes](../operate/storage-volumes.md) has the details.

Encryption is on by default: STATE, VAR and every volume are LUKS2 volumes whose key the
TPM seals to PCR 7. The
fallback, `spec.encryption.fallback` of the `SystemDisk`, decides what unlocks a volume when the
TPM does not:

| Fallback | What unlocks it | Cost |
| --- | --- | --- |
| `recovery-key` (default) | The node's recovery key, derived from the secrets file and printed by `chalkctl recovery-key` | Someone types the 64-letter key at the console, through a BMC or in person. |
| `password` | A password you give at install | You keep one more secret per node, outside the secrets file. |
| `none` | Nothing | A volume the TPM does not unseal stays locked until `chalkctl storage reset` recreates it empty; STATE and VAR cannot be reset that way, so for them it means reinstalling the node. |

Set `spec.encryption.mode: none` only for machines in a place you trust physically.
`chalkctl validate` then warns, because STATE holds the node's keys unencrypted.

## Decide who holds the Secure Boot key

Every image a node boots is signed with a Secure Boot db
key, and the firmware boots it only when the key's certificate is in db. chalkos does not enrol
keys in the firmware: you enrol the certificate yourself, through the firmware's setup or your
vendor's tools, before the first install. [Sign images for Secure Boot](../build/sign-images.md)
covers the keys and the enrolment.

Treat the db key as fixed for the cluster's lifetime. PCR 7 measures which db certificate
verified the boot, so an image signed with another key boots, if the firmware trusts it, and
then the TPM refuses to unseal STATE: every node asks for its recovery key. A firmware update
that changes db or dbx has the same effect.

Decide who signs. Whoever holds the db key runs
[`chalkbuild sign`](../reference/cli/chalkbuild_sign.md) on a build machine and hands out the
signed image directories, so the key stays in one place. The people who install and upgrade
nodes need no key: chalkctl sends the signed directories as they are, and each node checks the
signatures against its firmware's db and dbx and refuses an image the key did not sign.

## Protect the secrets file

[`chalkctl gen secrets`](../reference/cli/chalkctl_gen_secrets.md) writes the secrets file once.
It holds the OS CA and node CA
with their keys, the Kubernetes CAs and keys, and the secret every node's recovery key derives
from. Whoever holds it can install a node into the cluster, issue themselves any client
certificate and decrypt every node's disks with physical access.

Encrypt it with age to several recipients: each administrator's key, and one backup key kept
offline. Recipients may be age keys, SSH public keys or age plugin recipients, so hardware keys
work through a plugin such as age-plugin-yubikey, whose binary must be on the `PATH` of whoever
runs chalkctl.

```sh
chalkctl gen secrets --recipient=<admin-recipient> --recipient=<backup-recipient>
```

The command writes `secrets.age` and `secrets.pub.json`, the public half, which the
[`osCA`](../reference/options.md#osca) argument of `mkImages` names so the images trust the OS
CA. To decrypt the file, chalkctl tries `~/.config/chalkos/age.key`, `~/.ssh/id_ed25519` and
`~/.ssh/id_rsa`, or the files `--identity` names; a plugin identity file goes there too.
`--plaintext` writes `secrets.json` unencrypted, for a file protected by other means.

chalkctl reads the secrets file only where `--secrets` names it, and the inventory names none,
so the repository decides nothing about where the file lives. `secrets.pub.json` must be
tracked by git, because the flake reads it. Committing `secrets.age` puts it under the same
review and history as the rest of the cluster; every clone then carries it, though, so its
protection rests on the recipients' keys alone. A secret manager can keep it instead:
`gen secrets --out -` writes the file to standard output, and `--secrets -` reads it from
standard input.

```sh
chalkctl gen secrets --recipient=<admin-recipient> --out=- --public-out=secrets.pub.json \
  | pass insert --multiline chalkos/prod
pass show chalkos/prod | chalkctl apply-config w1 --secrets=-
```

Two kinds of files appear next to a secrets file on disk later.
[`chalkctl rotate`](../reference/cli/chalkctl_rotate.md) and
[`chalkctl node-ca rotate`](../reference/cli/chalkctl_node-ca_rotate.md) rewrite the file in
place and keep the previous version as `secrets.age.prev`, encrypted like the original and
holding the old keys. Every command that changes the file holds `secrets.age.lock` while it
runs. Leave both out of version control. The file's recipients change only when one of those
commands rewrites it with `--recipient`.

Back the file up to a second place. Without it you cannot install or reinstall a node, renew a node
certificate by hand, deliver config with `apply-config`, issue client files or kubeconfigs, rotate
anything or derive a recovery key. [Recover a node](../operate/recover-node.md#the-secrets-file-is-lost) lists
what still works.

## Give each person a client file

Status, logs, reboots, upgrades and the etcd commands do not need the secrets file; installs and
config changes do. A client file holds a certificate of one
client role for one person:

```sh
chalkctl config new --name=alice --role=operator --out=alice.json --secrets=secrets.age
```

| Client role | May |
| --- | --- |
| reader | read node information, disks, status, logs and etcd's members |
| operator | also reboot, upgrade, drain and uncordon nodes |
| admin | call every method but the one nodes renew their certificates with, including bootstrap, volume resets and etcd membership changes |

chalkctl reads a client file from `--config`, else from the file `$CHALKOSCONFIG` names, else
from `~/.config/chalkos/config`, whenever a command names no `--secrets`.
[Security model](../how-it-works/node/security.md#who-may-call-the-node-api) lists the methods each client
role may call. A client file is valid for a year unless `--ttl` says otherwise, and nothing
revokes a single one: taking access away means [rotating the OS CA](../operate/access.md#rotate-the-os-ca).
Issue them per person, with the shortest validity your routine tolerates, so a departure is a
rotation you plan rather than one you rush.

Kubeconfigs from [`chalkctl kubeconfig`](../reference/cli/chalkctl_kubeconfig.md) are the same
for the Kubernetes API: one per person with `--name`, all in the group bound to
`cluster-admin`, revoked only by rotating the Kubernetes CAs. Narrower access uses RBAC, as
[Bootstrap and get a kubeconfig](bootstrap.md#give-someone-access-to-the-kubernetes-api) describes.

## Choose time servers

Nodes check certificates against their clocks, so a node whose clock drifts refuses valid
certificates or accepts expired ones. chalkos runs chrony with the servers of the
[`TimeSync`](../reference/kinds.md#timesync) document, by default three servers of the German
national metrology institute (PTB), authenticated with NTS. NTS needs the nodes to reach the
servers on TCP port 4460 as well as UDP port 123.

A network without outbound NTP needs internal servers. Use NTS where your servers support it:

```yaml title="cluster/time.yaml"
apiVersion: time.chalkos.dev/v1alpha1
kind: TimeSync
metadata: {name: time}
spec:
  servers:
    - host: ntp1.example.internal
    - host: ntp2.example.internal
      nts: false
```

`spec.dhcp: true` adds the servers DHCP announces, which nothing authenticates.
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) changes either without a
new image.

## Plan registry mirrors

Nodes pull the control plane's images, etcd, CoreDNS and the pause image from `registry.k8s.io`,
and flannel from `ghcr.io`. A
[`Mirror`](../reference/kinds.md#mirror) document puts endpoints in front of one registry host;
containerd tries them in order and falls back to the registry. chalkd uses the same mirrors for
OS images, as [Pull through a mirror](../operate/registries.md#pull-through-a-mirror) describes. A
cluster without internet access needs every image in a mirror, including those the image options
[`chalkos.kubernetes.images`](../reference/options.md#chalkoskubernetesimagesetcd) and
[`chalkos.cni.flannel.image`](../reference/options.md#chalkoscniflannelimage) name.

```yaml title="cluster/registries.yaml"
apiVersion: oci.chalkos.dev/v1alpha1
kind: Mirror
metadata: {name: registry.k8s.io}
spec:
  registry: registry.k8s.io
  endpoints: [https://mirror.example.internal]
---
apiVersion: oci.chalkos.dev/v1alpha1
kind: Mirror
metadata: {name: ghcr.io}
spec:
  registry: ghcr.io
  endpoints: [https://mirror.example.internal]
```

Workloads from a private registry need a [`Registry`](../reference/kinds.md#registry) with that
registry's credentials in the same file. The kubelet then pulls the registry's images for every
pod on the node without `imagePullSecrets`, and containerd sends the credentials to the endpoints
of the registry's `Mirror` as well, as
[How do pods pull images from a private registry?](../how-it-works/kubernetes/kubernetes.md#how-do-pods-pull-images-from-a-private-registry)
describes. Keep credentials that only some workloads may use in their namespaces' pull secrets
instead.

## Plan provenance and upgrades

An image carries no version: its store's root hash is what
it is, so any change to an image, from a new chalkos input to a new Kubernetes release, makes a
new image that `chalkctl upgrade` installs without anything raised. The `revision` that
`flake.nix` above passes to `mkImages` records the commit each image was built from; a clean
checkout gives the commit, a checkout with changes the commit marked `-dirty`. Nix cannot read
git tags, so when a release is tagged, build it with `tag` as well, such as from a release job
that passes the tag it builds. `chalkctl status` and `chalkctl upgrade` show both beside the root
hash, as in `3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)`; nothing orders or refuses an image by them.

Each node numbers the boot entries it installs and boots the one installed last first. Going back
to an earlier image is therefore an upgrade like any other, which boots first as the highest
generation; it is allowed, at your own risk, and nothing warns of it.

A routine that works: change the flake, build the images into `images/` again, sign copies of them
into `-signed` directories with `chalkbuild sign`, run `chalkctl validate`, upgrade a lab of the same variants
with copies `chalklab sign` signed, then run `chalkctl upgrade` on the cluster, which gives each
node the image the inventory names for it. Kubernetes moves one
minor release at a time, as upstream requires. [Upgrade nodes](../operate/upgrade.md) describes
a run.

## Note when certificates expire

chalkos renews what it can by itself and warns in `chalkctl status` before anything else
expires.

| What | Lifetime | Renewed by |
| --- | --- | --- |
| OS CA | 10 years | [`chalkctl rotate os-ca`](../operate/access.md#rotate-the-os-ca); status warns a year ahead |
| Node CA | 5 years | `chalkctl node-ca rotate`; status warns 18 months ahead |
| Kubernetes CAs | 10 years | `chalkctl rotate kubernetes-ca`; status warns a year ahead |
| Node certificates | 1 year | chalkd, after two thirds of the lifetime; nodes without Kubernetes need `chalkctl node renew` |
| Control-plane certificates | 1 year | chalkd, after two thirds of the lifetime |
| Kubelet certificates | 1 year | the kubelet |
| Client files and kubeconfigs | `--ttl`, 1 year by default | issuing new ones |

A node whose image runs no Kubernetes has no control plane to renew its node certificate, so put
`chalkctl node renew` for those nodes in the yearly calendar.

## Plan backups

chalkos has no etcd backup or restore command yet. Three control planes protect against losing a
machine; they do not bring back an object someone deleted. Back up what your workloads keep with
tools that work at the Kubernetes level, and back up these yourself:

- the secrets file and its backup recipient's key;
- the cluster's repository: the inventory, the YAML files and `flake.lock`, which pins the
  chalkos version every image is built from;
- the Secure Boot db key and certificate;
- a password file for each node whose fallback is a password.

## Check that it worked

Build the image, sign a copy of it into the directory the inventory names, then check every
node's config against it.
[`chalkctl validate`](../reference/cli/chalkctl_validate.md) reaches no node and needs no secrets
file:

```sh
nix build .#node-metal -o images/node-metal
rm -rf images/node-metal-signed
chalkbuild sign --key db.key --cert db.crt images/node-metal images/node-metal-signed
chalkctl validate
```

`nix build` fails on what only the image knows, such as an unknown option. `chalkctl validate`
prints a line for each valid node, such as
`cp1: valid for the image node-metal (node on metal, root hash 2ece03b0fa09), config <hash>`,
names each node whose config is not valid with the reason, and warns of a node whose STATE is
unencrypted. Run both in CI, so a change to the flake or a YAML file never reaches a node
unchecked. CI needs no db key for that: `chalkctl validate` checks no signature, so CI links the
unsigned build to `images/node-metal-signed` with
`nix build .#node-metal -o images/node-metal-signed`. The unsigned build goes to the `-signed`
path only so that `chalkctl validate` finds the directory the inventory names.

## What next

- [Install a node](../operate/install.md) on a physical machine or a VM.
- [Run a highly available control plane](ha-control-plane.md).
- [Security model](../how-it-works/node/security.md) and [Certificates](../how-it-works/node/certificates.md)
  explain what the keys protect.
- [Storage and encryption](../how-it-works/node/storage.md) explains the volumes and their keys.
