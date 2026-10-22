---
title: "Glossary"
description: "The terms of the chalkos documentation, each with a short definition and the page that explains it"
---

# Glossary

Each entry defines a term the documentation uses repeatedly and links to the page that explains
it. Option, command and API names link to the generated reference.

## A/B { #a-b }

The update scheme of a node's disk: two [slots](#slot), A and B, each able to hold an
[image](#image). The node boots one slot while an [upgrade](#upgrade) writes the other, so the
image it ran before stays on disk for a [rollback](#rollback). See
[The image](../concepts/image.md).

## Admin { #admin }

The [client role](#client-role) that may call every method of the node API but the one nodes
renew their certificates with, which needs the role `node`: install, config changes, volume
resets, bootstrap, etcd membership changes and rotations among them. See
[Security model](../concepts/security.md).

## Blessed boot { #blessed-boot }

A boot that the [health check](#health-check) found healthy, after which `systemd-bless-boot`
removes the boot counter from the [UKI](#uki)'s file name. A blessed image is never rolled
back. See [Boot, health and rollback](../concepts/boot-and-rollback.md).

## Boot counting { #boot-counting }

systemd-boot's count of the tries a newly installed image has left, kept in its UKI's file name
(`chalkos_<generation>+3.efi`). Each boot that is not [blessed](#blessed-boot) uses a try; with
none left, systemd-boot boots the previous image. The number of tries is
[`chalkos.upgrade.bootTries`](options.md#chalkosupgradeboottries). See
[Boot, health and rollback](../concepts/boot-and-rollback.md).

## Bootstrap { #bootstrap }

The one-time start of a cluster's etcd on its first [control plane](#control-plane), with
[`chalkctl bootstrap`](cli/chalkctl_bootstrap.md). It is refused on a node that is bootstrapped
or holds etcd data, so a cluster is never initialised twice; further control planes join on
their own. See [Kubernetes on chalkos](../concepts/kubernetes.md).

## chalkcfg { #chalkcfg }

The command on every node that prints the node's [config documents](#config-document) from
`/run/chalkos/config`, by kind and name, as names, JSON, YAML, a jsonpath expression or a Go
template. It has no network access and never prints `Secret`s or `Addon`s. A variant's NixOS
modules build its commands with [`chalkos.kinds.<Kind>.get`](options.md#chalkoskindsget), which
checks a path against the kind's options when the image is evaluated. See
[its commands](cli/chalkcfg.md).

## chalkctl { #chalkctl }

The command-line client of chalkos. It reads a cluster from an [inventory](#inventory) and the
[image directories](#image-directory) it names for the nodes, installs, upgrades and operates nodes
through [chalkd](#chalkd), and issues certificates from the [secrets file](#secrets-file). It never
builds images or runs Nix. See [its commands](cli/chalkctl.md) and
[Architecture](../concepts/architecture.md).

## chalkd { #chalkd }

The agent on every node. It serves the [node API](api.md) on TCP port 50000 over mutual TLS,
installs and upgrades the node, applies its [config](#config), runs the Kubernetes control
plane and decides whether a boot is healthy. See [Architecture](../concepts/architecture.md).

## chalklab { #chalklab }

The command that runs a cluster's `kvm` nodes as QEMU virtual machines on one machine, each
with Secure Boot and a TPM, and installs and bootstraps them. See [its commands](cli/chalklab.md)
and the [quick start](../getting-started/index.md).

## Change rule { #change-rule }

A CEL expression an image's schemas declare on a [kind](#kind) or on one of its fields, which every
config the image accepts makes true. A rule that is false refuses the config, or says it takes
[`chalkctl storage reset`](cli/chalkctl_storage_reset.md) or a reinstall. See
[Change rules and facts](../concepts/rules.md).

## Client file { #client-file }

A JSON file holding a client certificate of one [client role](#client-role), its key and the
[OS CA](#os-ca) certificate, issued with
[`chalkctl config new`](cli/chalkctl_config_new.md). It lets someone operate nodes without the
[secrets file](#secrets-file). It names no cluster: its OS CA ties it to one, and a node of another
cluster fails the TLS handshake with it. See [Security model](../concepts/security.md).

## Client role { #client-role }

The permission a client certificate grants on the node API, taken from its Organization:
[reader](#reader), [operator](#operator) or [admin](#admin), each allowed what the roles before
it are. A certificate issued through the [node CA](#node-ca) has the role `node`, which may
only renew its node certificate. See [Security model](../concepts/security.md).

## Cluster endpoint { #cluster-endpoint }

The URL of the cluster's API server, `spec.endpoint` of the [`Cluster`](kinds.md#cluster)
document, at which kubelets, kubeconfigs and joining control planes reach it. Nodes also renew
their [node certificates](#node-certificate) at port 50000 of its host, so it points at a control
plane, a [VIP](#vip) or a load balancer that forwards both ports. See
[Networking](../concepts/networking.md).

## Config { #config }

A node's configuration at runtime: a set of [config documents](#config-document), each of a
[kind](#kind) its image accepts, such as `Node`, `Network`, `SystemDisk`, `Cluster` or an
extension's own. The [inventory](#inventory) names the YAML files whose documents form a node's
set. chalkctl checks the set against the schemas of the node's image and delivers it at install
and with [`chalkctl apply-config`](cli/chalkctl_apply-config.md); chalkd checks it again, keeps it
on [STATE](#state) and applies it at every boot. A new set replaces the node's previous one as a
whole. Secrets travel as `Secret` documents and reach only the services that read the kinds
referencing them. See [Config and the inventory](../concepts/config.md).

## Config document { #config-document }

One object of a node's [config](#config), written in YAML in the style of a Kubernetes object: an
`apiVersion` of a group and a version, a `kind`, a `metadata.name` and a `spec`. The schema of
its [kind](#kind) defines the `spec`; [Config kinds](kinds.md) lists the fields of the built-in
kinds. On the node, chalkd writes each document except `Secret`s and `Addon`s to
`/run/chalkos/config/<group>/<Kind>/<name>.json`. See
[Config and the inventory](../concepts/config.md#what-is-a-config-document).

## Contract { #contract }

What an image accepts as [config](#config): the variant and platform it was built for, the JSON
Schemas of its [kinds](#kind) and what it fixes about Kubernetes. The image carries the schemas in
`/etc/chalkos/schemas` and the Kubernetes facts in `/etc/chalkos/kubernetes/image.json`, and its
[image directory](#image-directory) has them as `schemas/` and `image.json`. chalkctl reads the
contract from the image directory when it validates config, installs from the
[installer](#installer) or upgrades a node, and from the running node with the
[Contract](api.md#method-contract) method when it applies config. On an upgrade, the node reads
it from the image it wrote. See [Image and partition layout](image-layout.md).

## Control plane { #control-plane }

A node whose `Node` document sets `spec.type: controlplane`. It runs etcd, the API server, the
controller-manager and the scheduler as static pods that chalkd renders, besides containerd and
the kubelet, and it is the only kind of node chalkctl delivers `Addon` documents to. See
[Kubernetes on chalkos](../concepts/kubernetes.md).

## db and dbx { #db-and-dbx }

The UEFI firmware's Secure Boot databases: db lists the certificates and hashes of binaries the
firmware boots, dbx those it refuses. A chalkos image boots under Secure Boot when its signer's
certificate is in db and nothing of it is in dbx. See [Security model](../concepts/security.md).

## dm-verity { #dm-verity }

The kernel's block-level integrity check: each block of the [store](#store) is hashed into a
hash tree whose top is the [root hash](#root-hash), and a block that does not match fails to
read. See [The image](../concepts/image.md).

## Effect { #effect }

What a delivery does to the node's services once it is applied: restart or reload units, run a
command or act on chalkd's loops, as [`chalkos.effects`](options.md#chalkoseffects) declares it.
An effect fires when one of its [outputs](#output) or the documents of one of its kinds changed,
or when its CEL rule, like a [change rule](#change-rule), holds. See
[Outputs and effects](../concepts/outputs-and-effects.md#what-is-an-effect).

## ESP { #esp }

The EFI system partition, a FAT file system at the start of the system disk. It holds
systemd-boot and the [UKIs](#uki) of both [slots](#slot), and is 1 GiB by default. See
[Image and partition layout](image-layout.md).

## etcd member { #etcd-member }

A control plane that belongs to the cluster's etcd, as a learner while it catches up and as a voter
after. A control plane pins its addresses on [STATE](#state) when it bootstraps, or before it adds
itself as a learner, and every later boot waits for exactly them;
[`chalkctl etcd leave`](cli/chalkctl_etcd_leave.md) undoes both. See [Kubernetes on
chalkos](../concepts/kubernetes.md).

## Extension { #extension }

NixOS modules outside chalkos, in a [variant](#variant)'s modules, that add services to its images
and may declare config [kinds](#kind) under [`chalkos.kinds`](options.md#chalkoskinds), whose
documents a node's YAML files then carry. The services read those documents through a
[grant](#grant) or as [outputs](#output) rendered from them, and [effects](#effect) restart them
when they change. chalkos's own Kubernetes support declares its kinds and effects the same way. See
[Variants and platforms](../concepts/variants.md#how-does-an-extension-add-a-kind).

## Fact { #fact }

Node state a [change rule](#change-rule) reads, declared in
[`chalkos.facts`](options.md#chalkosfacts): the JSON of a file, such as the
[Kubernetes share](#kubernetes-share), or of a program of the image's store, which chalkd runs in
a sandbox. See [Change rules and facts](../concepts/rules.md#what-is-a-fact).

## Fallback keyslot { #fallback-keyslot }

The second keyslot of every encrypted partition, which unlocks it when the [TPM](#tpm) does not:
the node's [recovery key](#recovery-key) by default, a password, or none, as
`spec.encryption.fallback` of the node's [`SystemDisk`](kinds.md#systemdisk) chooses. See
[Storage and encryption](../concepts/storage.md).

## Generation { #generation }

The number in a [UKI](#uki)'s file name on the [ESP](#esp), `chalkos_<generation>.efi`, which a
node counts itself. The image and an install write generation 1; each [upgrade](#upgrade) writes
the one above the highest on the ESP, so systemd-boot, which orders a node's UKIs by file name,
boots the image installed last first, older or newer. See
[Boot, health and rollback](../concepts/boot-and-rollback.md#how-does-systemd-boot-choose-an-entry).

## Grant { #grant }

The [config](#config) kinds a unit reads at runtime, as
[`systemd.services.<unit>.chalkos.kinds`](options.md#systemdserviceschalkoskinds) declares them:
chalkd writes their documents below `/run/chalkos/config` and the values their `secretRef` fields
name into the unit's credentials. A grant restarts nothing; an [effect](#effect) does. See
[Outputs and effects](../concepts/outputs-and-effects.md#what-does-a-grant-give-a-unit).

## Health check { #health-check }

The check `chalkos-health.service` runs on a counted boot. chalkd must serve on every node, and on
a node with Kubernetes the preparation must have succeeded. Then a control plane that is an etcd
member needs its local etcd member, its API server and its kubelet healthy, and a worker its
kubelet healthy and its Node registered. A node without Kubernetes, or a control plane not
bootstrapped yet, needs its config applied and no unit of the boot failed. A boot that passes
within [`chalkos.upgrade.healthTimeout`](options.md#chalkosupgradehealthtimeout) is
[blessed](#blessed-boot). See [Boot, health and rollback](../concepts/boot-and-rollback.md).

## Identity { #identity }

The form chalkd translates a node's [config](#config) into before it applies it: hostname,
static addresses, labels, taints, storage and Kubernetes addresses, without secrets.
chalkd keeps it on [STATE](#state) in each image's config slot, translated with that image's
schemas, and the image's boot applies it.
`chalkctl validate` translates a set the same way to check it. See
[Image and partition layout](image-layout.md#files-on-state).

## Image { #image }

The whole operating system of a node as one unsigned raw disk image, with the ESP and slot A
filled, built by `chalkos.lib.mkImages` for one [variant](#variant) on one
[platform](#platform). Every node that runs it boots the same bytes; what differs between nodes
travels in their [config](#config). Its build output is an [image directory](#image-directory).
See [The image](../concepts/image.md).

## Image directory { #image-directory }

The build output of an [image](#image), `images.<variant>-<platform>` of `chalkos.lib.mkImages`: the
raw image, systemd-repart's report of its partitions, the definitions of the system region,
`metadata.json`, which names the image's variant, platform, [root hash](#root-hash),
[provenance](#provenance) and kinds, the kinds' JSON Schemas in `schemas/` and, on an image with
Kubernetes, what it fixes about Kubernetes in `image.json`. The [inventory](#inventory) names it in
an image's `source.directory`, and chalkctl reads it to validate config, sign, install and upgrade.
See [Image and partition layout](image-layout.md#outputs-of-an-image-build).

## Installer { #installer }

An image that always boots into [maintenance mode](#maintenance-mode) and writes a node's image
to a disk of the machine. `chalkos.lib.mkImages` builds one, as a raw image and a hybrid ISO, with
[`installer.modules`](options.md#installermodules); it installs images of every variant and
platform. See [Architecture](../concepts/architecture.md).

## Inventory { #inventory }

The file that tells chalkctl about a cluster: each node by name, the address chalkctl reaches it at,
the [image](#image) it runs, and the YAML files whose documents form its [config](#config), as files
every node gets, files of named groups and each node's own. The inventory names each image by its
[image directory](#image-directory), relative to the inventory's directory; a node runs its own
`image`, else that of its groups, else the inventory's `spec.image`. Every node that holds a
`Cluster` document holds the same `Cluster` and `Flannel` specs. chalkctl reads `chalkos.yaml` in
the current directory, or the file `--inventory` names. It stays on the operator's machine and
names no secrets. Its keys of `spec.nodes` are the [node names](#node-name); its `metadata.name` is
a label for messages and the default kubeconfig context. See
[Config and the inventory](../concepts/config.md#how-does-the-inventory-assign-files-to-nodes) and
its fields in [Config kinds](kinds.md#inventory).

## Kind { #kind }

The type of a [config document](#config-document), named by its `kind` and the group and version
of its `apiVersion`. chalkos declares its own kinds in `chalkos.dev` and its subdomains; an
[extension](#extension) declares more under [`chalkos.kinds`](options.md#chalkoskinds) in a
variant's NixOS modules, with a group of its own. An image accepts the kinds its modules declare
and refuses any other. A unit reads the documents of the kinds it is [granted](#grant). See
[Config and the inventory](../concepts/config.md#which-kinds-are-there) and
[Config kinds](kinds.md).

## Kubernetes share { #kubernetes-share }

The Kubernetes secrets a node receives at install: on a control plane, the cluster, front-proxy
and etcd CAs with their keys, the service-account key, the encryption key and the node CA; on a
worker, the cluster CA certificate and a kubelet client certificate. chalkd keeps it on
[STATE](#state). See [Certificates](../concepts/certificates.md).

## Lab { #lab }

A cluster that [chalklab](#chalklab) runs as virtual machines, from a lab file, `chalklab.yaml`,
that names the cluster's [inventory](#inventory) and each VM's MAC address, memory, CPUs and
disks. Its state (disks, firmware variables, TPM state, keys, logs, kubeconfig, client file and an
inventory that reaches the nodes at forwarded ports) lives in `$XDG_STATE_HOME/chalklab/<name>`,
after the lab file's `metadata.name`. See the [quick start](../getting-started/index.md) and
[Config kinds](kinds.md#lab).

## Maintenance mode { #maintenance-mode }

The mode chalkd runs in on a node that is not installed: it serves a self-signed certificate,
prints its fingerprint and the node's addresses on the console, and accepts only reads,
reboots and an install. See [Architecture](../concepts/architecture.md).

## Module group { #module-group }

A named set of kernel modules an [image](#image) carries, listed in
[`chalkos.kernel.moduleGroups`](options.md#chalkoskernelmodulegroups). The base groups
`storage`, `network`, `virtualisation`, `filesystems`, `kubernetes` and `platform` are on by
default; `gpu`, `sound`, `media`, `wireless`, `infiniband`, `can` and `industrial` are added in a
variant's or platform's modules. See [The image](../concepts/image.md).

## Node { #node }

A machine of the cluster, named in the [inventory](#inventory), which also names the [image](#image)
it runs. On an image with Kubernetes, its `Node` document of the group `kubernetes.chalkos.dev`
says whether it is a [control plane](#control-plane) or a [worker](#worker), and gives its labels,
taints and Kubernetes addresses. It is installed from that image's
[image directory](#image-directory). See [Config and the inventory](../concepts/config.md).

## Node CA { #node-ca }

An intermediate CA under the [OS CA](#os-ca) that may issue only server and client
certificates. Control planes hold it and renew every node's certificate with it, so they never
hold the OS CA's key. See [Certificates](../concepts/certificates.md).

## Node certificate { #node-certificate }

The certificate chalkd serves and authenticates with, issued by the [node CA](#node-ca) for the
node's name, host name and addresses and valid for one year. chalkd takes the node's name from its
subject and renews it through a control plane once two thirds of its lifetime have passed. See
[Certificates](../concepts/certificates.md).

## Node name { #node-name }

A [node](#node)'s key in the [inventory](#inventory)'s `spec.nodes`, such as `cp1`. chalkctl puts it
into the [node certificate](#node-certificate) at install, and chalkd reads it from there. The
hostname defaults to it unless a `Host` document gives another, and the Kubernetes node name, the
etcd member, the seed of the partition UUIDs and the [recovery key](#recovery-key) derive from it,
so renaming a node takes a reinstall. See
[Config and the inventory](../concepts/config.md#what-names-a-node).

## Normal mode { #normal-mode }

The mode chalkd runs in on an installed node: it serves its node certificate and accepts only
clients with a certificate from the [OS CA](#os-ca). See
[Architecture](../concepts/architecture.md).

## Operator { #operator }

The [client role](#client-role) that may do what a [reader](#reader) may, and also reboot,
upgrade, drain and uncordon nodes; an upgrade that delivers config needs an [admin](#admin). See
[Security model](../concepts/security.md).

## OS CA { #os-ca }

The root certificate authority of a cluster's node API: chalkd and chalkctl trust only
certificates that chain to it. It lives in the [secrets file](#secrets-file), is valid for ten
years and signs the [node CA](#node-ca) and client certificates. See
[Certificates](../concepts/certificates.md).

## Output { #output }

A set of config files for a service, rendered from documents by a program of the image's store
and checked before a delivery changes the node, as [`chalkos.outputs`](options.md#chalkosoutputs)
declares it. chalkd renders every output again at boot. See
[Outputs and effects](../concepts/outputs-and-effects.md#what-is-an-output).

## PCR 7 { #pcr-7 }

The TPM register that measures the Secure Boot state: whether it is on, the PK, KEK, db and dbx
contents, and the db certificate that verified the boot binaries. chalkos seals the keys of
encrypted partitions to it, so they unseal after an upgrade signed with the same key and not
after a change to Secure Boot. See [Security model](../concepts/security.md).

## Platform { #platform }

The kind of machine an [image](#image) is built for, such as `metal` or `kvm`, named in the
[`platforms`](options.md#platforms) argument of `chalkos.lib.mkImages` with NixOS modules of its
own. Every [variant](#variant) gets one image per platform, so only that platform's images carry
its drivers and agents. A node refuses an image of another platform than the one it runs. See
[Variants and platforms](../concepts/variants.md#what-is-a-platform).

## Provenance { #provenance }

The git revision and tag an [image](#image) was built from, which `chalkos.lib.mkImages` takes
as `revision` and `tag` and writes into the image's `metadata.json`. chalkctl shows them beside
the [root hash](#root-hash), as in `3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)`, and a node keeps them in
`/state/chalkd/images.json`. They never reach the store, and nothing orders or refuses an image by
them. See [The image](../concepts/image.md#what-identifies-an-image).

## Reader { #reader }

The [client role](#client-role) that may read: node information, disks, status, logs and etcd
members. See [Security model](../concepts/security.md).

## Recovery key { #recovery-key }

The default [fallback keyslot](#fallback-keyslot) of every encrypted partition, used when the
TPM does not unseal. chalkos derives it from the [secrets file](#secrets-file) and the
[node name](#node-name), so the file never changes, and
[`chalkctl recovery-key`](cli/chalkctl_recovery-key.md) prints it. See
[Storage and encryption](../concepts/storage.md).

## Rollback { #rollback }

The return to the previous image after a new one used up its boot tries without a
[blessed boot](#blessed-boot). chalkd reports it in status with the failed boot's last log
lines, and `chalkctl upgrade` stops. See
[Boot, health and rollback](../concepts/boot-and-rollback.md).

## Root hash { #root-hash }

The top hash of the [store](#store)'s dm-verity hash tree. The UKI carries it on the kernel
command line (`usrhash=`), so the UKI's signature covers every byte of the store. It identifies an
image: chalkd and chalkctl tell images apart by it alone, and chalkctl shows its first 12 hex
characters. See [The image](../concepts/image.md#what-identifies-an-image).

## Rotation { #rotation }

The replacement of a CA or key with [`chalkctl rotate`](cli/chalkctl_rotate.md), in phases that
first trust the new value everywhere, then use it, then re-issue, and last remove the old value.
Rotation is also how access is revoked. See [Certificates](../concepts/certificates.md).

## Secrets file { #secrets-file }

The file [`chalkctl gen secrets`](cli/chalkctl_gen_secrets.md) writes once with every secret of
a cluster: the OS CA and node CA, the Kubernetes CAs and keys, and the secret recovery keys
derive from. It is `secrets.age`, encrypted with age, or `secrets.json`; its public part is
`secrets.pub.json`. chalkctl reads it only where `--secrets` names it, or from standard input
with `--secrets -`; an inventory names none. See [Security model](../concepts/security.md).

## Secure Boot { #secure-boot }

The UEFI firmware's check that a boot binary is signed by a certificate in [db](#db-and-dbx).
chalkos signs systemd-boot and the [UKI](#uki), whose signature covers the store through the
[root hash](#root-hash). See [Security model](../concepts/security.md).

## Slot { #slot }

A pair of partitions that holds one image's [store](#store): the erofs data and its dm-verity
hash tree, labelled `store_<hash16>` and `store-verity_<hash16>` from the first 16 hex characters
of its [root hash](#root-hash), or `_empty`. A node has two,
A and B. See [The image](../concepts/image.md).

## STATE { #state }

A small partition, 128 MiB by default, encrypted and sealed to the TPM by default. It holds the
node's config, node certificate, trusted CAs, Kubernetes share and storage definitions, and
the marker that the node is installed. See [Storage and encryption](../concepts/storage.md).

## Store { #store }

The read-only Nix store of an image: an erofs file system compressed with zstd, checked by
dm-verity on every read. See [The image](../concepts/image.md).

## TPM { #tpm }

The machine's Trusted Platform Module 2.0, which seals the keys of [STATE](#state),
[VAR](#var) and encrypted volumes to [PCR 7](#pcr-7), so they unlock without a password only
under the same Secure Boot state. See [Security model](../concepts/security.md).

## UKI { #uki }

A unified kernel image: the kernel, initrd, kernel command line and os-release in one EFI
binary, which systemd-boot loads and Secure Boot verifies. chalkos keeps one per slot on the
ESP as `chalkos_<generation>.efi`. See [The image](../concepts/image.md).

## Upgrade { #upgrade }

The installation of another image into a node's inactive [slot](#slot), from which it
boots next with [boot counting](#boot-counting). [`chalkctl upgrade`](cli/chalkctl_upgrade.md)
rolls it through a cluster. A Kubernetes upgrade is an image upgrade. See
[Upgrades](../concepts/upgrades.md).

## VAR { #var }

The partition mounted at `/var` that holds what a node writes: etcd, containerd, the kubelet,
logs. The node's [`SystemDisk`](kinds.md#systemdisk) document sizes it; by default it fills the
rest of the system disk, and it is encrypted like [STATE](#state). See
[Storage and encryption](../concepts/storage.md).

## Variant { #variant }

A named set of NixOS modules that `chalkos.lib.mkImages` builds into one [image](#image) per
[platform](#platform), `images.<variant>-<platform>`, given in its
[`variants`](options.md#variants) argument: for example one variant for most nodes and one with
GPU drivers. Control planes and workers can run the same variant. Its name is `CHALKOS_VARIANT`
in the image's os-release and `variant` in its `metadata.json`. No check compares it: a node may
change its variant through an [upgrade](#upgrade), and the new image's schemas decide which
[config](#config) it takes. See
[Variants and platforms](../concepts/variants.md#what-is-a-variant).

## VIP { #vip }

A virtual IP address of the API server, one per IP family, that one healthy control plane
holds. chalkd elects the holder through etcd and announces the address on the local network.
See [Networking](../concepts/networking.md).

## Volume { #volume }

A partition beyond [VAR](#var), declared by a [`Volume`](kinds.md#volume) document in a node's
config, on the system disk or a disk of its own, encrypted or not, formatted and mounted or left
raw. See [Storage and encryption](../concepts/storage.md).

## Worker { #worker }

A node whose `Node` document sets `spec.type: worker`. It runs containerd and the kubelet, which
joins the cluster at the [cluster endpoint](#cluster-endpoint) with the certificate in its
[Kubernetes share](#kubernetes-share). See [Kubernetes on chalkos](../concepts/kubernetes.md).
