---
title: "Plan a production cluster"
description: "Decide the variants, addresses, storage, keys and time sources of a production cluster before installing it"
---

# Plan a production cluster

A production cluster needs decisions that the [quick start](../get-started/quick-start.md)'s lab
makes for you. A few of them cannot change once the cluster runs: the IP families, the control
planes' addresses, the secrets file and the
Secure Boot signing key. This page lays out the
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

## Choose the number of control planes

Use three control planes. etcd needs a majority of its voters, so three survive the loss of one
machine, and [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) can reboot them one at a
time while the API server keeps answering.

| Control planes | Survives the loss of | Upgrades and reboots |
| --- | --- | --- |
| 1 | none | The API server is down while the node reboots; `chalkctl upgrade` needs `--allow-downtime`. |
| 2 | none | Both must run for a quorum, so two are less available than one; `--allow-downtime` again. |
| 3 | one | One at a time, with no API downtime. |
| 5 | two | One at a time. Each member adds etcd write latency. |

Four voters tolerate one failure, like three, so an even number buys nothing. A single control
plane is fine for a lab or a cluster whose API may go away during upgrades. Small clusters can
run workloads on the control planes with `allowSchedulingOnControlPlanes: true` in the
[`Cluster`](../reference/kinds.md#cluster) document; otherwise they are tainted.

A node is a control plane when its [`Node`](../reference/kinds.md#node) document says
`type: controlplane`, and a worker with `type: worker`:

```yaml title="nodes/cp1.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: controlplane
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec:
  disk: {path: /dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-uplink}
spec:
  match: {name: enp1s0}
  network: {address: [10.0.0.11/24], gateway: 10.0.0.1}
```

[Run a highly available control plane](ha-control-plane.md) walks through installing three.

## Decide how clients reach the API server

The cluster endpoint, `spec.endpoint` of the
[`Cluster`](../reference/kinds.md#cluster) document, is the URL kubelets, kubeconfigs and
workers use to reach the control planes. Workers also renew their
node certificates through it:
chalkd on a worker connects to the endpoint's host on port
50000, where a control plane's chalkd answers. So whatever the endpoint points at must reach
chalkd on 50000 as well as the API server on 6443.

Two ways make the endpoint survive a control plane's failure:

- A VIP, which chalkd holds on one healthy control plane. It
  needs the control planes on one layer-2 segment, because the holder announces the address
  with gratuitous ARP and unsolicited neighbour advertisements. This is the default choice for
  machines in one rack or VLAN. The endpoint is the VIP, or a host name that resolves to it.
- An external load balancer, for control planes in different segments. It must forward port
  50000 too; a balancer that forwards only 6443 leaves workers unable to renew their
  certificates.

A VIP failover takes seconds: the holder's lease lives 10 seconds, and the HA end-to-end test
requires the VIP to move within 30 seconds of its holder's link going down. Clients with an open
connection to the old holder notice only when it times out.

The VIP is `spec.vip.addresses` of the `Cluster` document every node gets, and the endpoint
points at it:

```yaml title="cluster/kubernetes.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec:
  # The VIP the control planes hold.
  endpoint: https://10.0.0.10:6443
  vip: {addresses: [10.0.0.10]}
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Flannel
metadata: {name: default}
spec:
  # Nodes accept the pod network's VXLAN traffic only from the nodes' own network.
  vxlanSourceSubnets: [10.0.0.0/24]
```

A control plane adds the VIP to the interface holding its address of the VIP's family. One that
should carry it on another interface names that one in `spec.vip.interface` of its own `Node`
document, since interface names differ from machine to machine.

[Networking](../how-it-works/node/networking.md) explains the election and the address selection.

## Fix the IP families

`spec.ipFamilies` of the `Cluster` document lists the cluster's address families, the
primary one first: `[ipv4]` by default, `[ipv4, ipv6]` for dual stack. The families and their
order are fixed when the cluster is created. A control plane compares them with the families it
was pinned with and refuses to prepare Kubernetes when they differ:

```text
the cluster was created with families [ipv4]; ipFamilies is [ipv4 ipv6]; changing the families of a running cluster is not supported
```

A different choice later means a new cluster, so decide now. Pick the pod and service ranges of
each family at the same time, so they overlap none of your networks. A dual-stack cluster's
document names both families and one range of each:

```yaml
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec:
  endpoint: https://10.0.0.10:6443
  ipFamilies: [ipv4, ipv6]
  podCIDRs:
    ipv4: 10.244.0.0/16
    ipv6: fd00:10:244::/56
  serviceCIDRs:
    ipv4: 10.96.0.0/12
    ipv6: fd00:10:96::/112
```

## Plan the node addresses

Give every node a static address in a [`Network`](../reference/kinds.md#network) document of its
own file, as `nodes/cp1.yaml` above does, and the same address as its `address` in the
inventory. Two things depend on it:

- A control plane's addresses are pinned on STATE when it
  becomes an etcd member, and every later boot waits for
  exactly those addresses. A control plane cannot change its address while it runs; it leaves
  etcd and is reinstalled.
- chalkctl reaches a node at the address the inventory names for it, or at `--address`. It does
  not read the `Network` documents, so the two must agree.

Workers may take their address from DHCP instead and pick it with `spec.nodeIP.validSubnets` of
the `Cluster` document, or `spec.nodeIP.validSubnets` of a node's own
[`Node`](../reference/kinds.md#node) document, which replaces the cluster's. The inventory names
such a node by an address that stays, such as a DNS name.

Set `spec.vxlanSourceSubnets` of the [`Flannel`](../reference/kinds.md#flannel) document to the
ranges your nodes live in, as `cluster/kubernetes.yaml` above does. Without it, nodes accept the
pod network's VXLAN traffic from any source.
[`chalkctl validate`](../reference/cli/chalkctl_validate.md) refuses a node whose fixed addresses,
`nodeIP.addresses` of its `Node` or else the first static address of each family of its `Network`
documents, or whose `nodeIP.validSubnets` lie outside these ranges.

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
`cluster-admin`, revoked only by rotating the Kubernetes CAs. For narrower access, configure an
authenticator of the API server through `spec.extraArgs.kube-apiserver` of the `Cluster`
document.

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
