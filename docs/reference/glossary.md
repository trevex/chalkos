---
title: "Glossary"
description: "The terms of the chalkos documentation, each with a short definition and the page that explains it"
---

# Glossary

<!--
Scope: Every term the documentation uses repeatedly, defined in one or two sentences, with a
link to the concept page that explains it. Pages link a term here on its first use.
-->

Each entry defines a term the documentation uses repeatedly and links to the page that explains
it. Option, command and API names link to the generated reference.

## A/B { #a-b }

The update scheme of a node's disk: two [slots](#slot), A and B, each able to hold an
[image](#image). The node boots one slot while an [upgrade](#upgrade) writes the other, so the
image it ran before stays on disk for a [rollback](#rollback). See
[The image](../concepts/image.md).

## Admin { #admin }

The [client role](#client-role) that may call every method of the node API, including
install, identity changes, volume resets, bootstrap, etcd membership changes and rotations. See
[Security model](../concepts/security.md).

## Blessed boot { #blessed-boot }

A boot that the [health check](#health-check) found healthy, after which `systemd-bless-boot`
removes the boot counter from the [UKI](#uki)'s file name. A blessed image is never rolled
back. See [Boot, health and rollback](../concepts/boot-and-rollback.md).

## Boot counting { #boot-counting }

systemd-boot's count of the tries a newly installed image has left, kept in its UKI's file name
(`chalkos_<version>+3.efi`). Each boot that is not [blessed](#blessed-boot) uses a try; with
none left, systemd-boot boots the previous image. The number of tries is
[`chalkos.upgrade.bootTries`](options.md#chalkosupgradeboottries). See
[Boot, health and rollback](../concepts/boot-and-rollback.md).

## Bootstrap { #bootstrap }

The one-time start of a cluster's etcd on its first [control plane](#control-plane), with
[`chalkctl bootstrap`](cli/chalkctl_bootstrap.md). It is refused on a node that is bootstrapped
or holds etcd data, so a cluster is never initialised twice; further control planes join on
their own. See [Kubernetes on chalkos](../concepts/kubernetes.md).

## chalkctl { #chalkctl }

The command-line client of chalkos. It reads the [cluster definition](#cluster-definition)
through its [manifest](#manifest), installs, upgrades and operates nodes through
[chalkd](#chalkd), and issues certificates from the [secrets file](#secrets-file). See
[its commands](cli/chalkctl.md) and [Architecture](../concepts/architecture.md).

## chalkd { #chalkd }

The agent on every node. It serves the [node API](api.md) on TCP port 50000 over mutual TLS,
installs and upgrades the node, applies its [identity](#identity), runs the Kubernetes control
plane and decides whether a boot is healthy. See [Architecture](../concepts/architecture.md).

## chalklab { #chalklab }

The command that runs a cluster's `kvm` nodes as QEMU virtual machines on one machine, each
with Secure Boot and a TPM, and installs and bootstraps them. See [its commands](cli/chalklab.md)
and the [quick start](../getting-started/index.md).

## Client file { #client-file }

A JSON file holding a client certificate of one [client role](#client-role), its key, the
[OS CA](#os-ca) certificate and the cluster's node addresses, issued with
[`chalkctl config new`](cli/chalkctl_config_new.md). It lets someone operate nodes without the
[secrets file](#secrets-file). See [Security model](../concepts/security.md).

## Client role { #client-role }

The permission a client certificate grants on the node API, taken from its Organization:
[reader](#reader), [operator](#operator) or [admin](#admin), each allowed what the roles before
it are. A certificate issued through the [node CA](#node-ca) has the role `node`, which may
only renew its node certificate. See [Security model](../concepts/security.md).

## Cluster definition { #cluster-definition }

The Nix modules that declare a cluster: its settings, [roles](#role), [platforms](#platform) and
[nodes](#node) under the `chalkos.*` options, evaluated by `chalkos.lib.mkCluster` and exposed
by a [flake](#flake). It is the only inventory chalkctl reads. See
[The cluster definition](../concepts/cluster-definition.md).

## Control plane { #control-plane }

A node whose role has the Kubernetes kind `controlplane`. It runs etcd, the API server, the
controller manager and the scheduler as static pods that chalkd renders, besides containerd and
the kubelet. See [Kubernetes on chalkos](../concepts/kubernetes.md).

## db and dbx { #db-and-dbx }

The UEFI firmware's Secure Boot databases: db lists the certificates and hashes of binaries the
firmware boots, dbx those it refuses. A chalkos image boots under Secure Boot when its signer's
certificate is in db and nothing of it is in dbx. See [Security model](../concepts/security.md).

## dm-verity { #dm-verity }

The kernel's block-level integrity check: each block of the [store](#store) is hashed into a
hash tree whose top is the [root hash](#root-hash), and a block that does not match fails to
read. See [The image](../concepts/image.md).

## ESP { #esp }

The EFI system partition, a FAT file system at the start of the system disk. It holds
systemd-boot and the [UKIs](#uki) of both [slots](#slot), and is 1 GiB by default. See
[Image and partition layout](image-layout.md).

## etcd member { #etcd-member }

A control plane that belongs to the cluster's etcd, as a learner while it catches up and as a
voter after. Once a node is a member, its addresses are pinned on [STATE](#state) and every
later boot waits for exactly them; [`chalkctl etcd leave`](cli/chalkctl_etcd_leave.md) undoes
it. See [Kubernetes on chalkos](../concepts/kubernetes.md).

## Extension { #extension }

A cluster module outside chalkos that adds options under its own `chalkos.<name>` namespace,
optionally per node, and contributes NixOS modules to roles. chalkos's own features use the
same mechanism. See [The cluster definition](../concepts/cluster-definition.md).

## Flake { #flake }

A Nix flake that exposes a cluster as `chalkos.<cluster>`, the result of `mkCluster`, and pins
the chalkos version through `flake.lock`. chalkctl and chalklab evaluate it to read the cluster.
See [The cluster definition](../concepts/cluster-definition.md).

## Health check { #health-check }

The check `chalkos-health.service` runs on a counted boot: on a control plane, the local etcd
member, the API server and the kubelet are healthy; on a worker, the kubelet is healthy and its
Node registered; on a node without Kubernetes, chalkd serves and no unit failed. A boot that
passes within [`chalkos.upgrade.healthTimeout`](options.md#chalkosupgradehealthtimeout) is
[blessed](#blessed-boot). See [Boot, health and rollback](../concepts/boot-and-rollback.md).

## Identity { #identity }

A node's part of the cluster definition, without secrets: hostname, network units, labels,
taints, storage, platform, time servers and extension values. chalkctl delivers it at install
and with [`chalkctl apply-identity`](cli/chalkctl_apply-identity.md); chalkd keeps it on
[STATE](#state) and applies it at every boot. See
[The cluster definition](../concepts/cluster-definition.md).

## Image { #image }

A role image: one unsigned raw disk image per [role](#role) and [platform](#platform), with the
ESP and slot A filled. Every node of that role and platform boots the same image; what differs
between nodes travels in their [identity](#identity). See [The image](../concepts/image.md).

## Image version { #image-version }

The version an image is built with (`system.image.version`). It names the UKI file and the
slot's partition labels and appears in os-release; an image whose version is installed with a
different root hash is refused. See [Upgrades](../concepts/upgrades.md).

## Installer { #installer }

An image that always boots into [maintenance mode](#maintenance-mode) and writes a role image to
a disk of the machine. It is built as a raw image and a hybrid ISO from
[`chalkos.installer`](options.md#chalkosinstallerimage), and serves every role and platform.
See [Architecture](../concepts/architecture.md).

## Kubernetes share { #kubernetes-share }

The Kubernetes secrets a node receives at install: on a control plane, the cluster, front-proxy
and etcd CAs with their keys, the service-account key, the encryption key and the node CA; on a
worker, the cluster CA certificate and a kubelet client certificate. chalkd keeps it on
[STATE](#state). See [Certificates](../concepts/certificates.md).

## Lab { #lab }

A cluster that [chalklab](#chalklab) runs as virtual machines, with its state (disks, firmware
variables, TPM state, keys, logs, kubeconfig and client file) in
`$XDG_STATE_HOME/chalklab/<cluster>`. See the [quick start](../getting-started/index.md).

## Maintenance mode { #maintenance-mode }

The mode chalkd runs in on a node that is not installed: it serves a self-signed certificate,
prints its fingerprint and the node's addresses on the console, and accepts only reads,
reboots and an install. See [Architecture](../concepts/architecture.md).

## Manifest { #manifest }

The JSON document `mkCluster` generates from the cluster definition (`chalkos.<cluster>.manifest`):
the cluster's settings, its roles' image paths and every node's identity. It is how Go code
reads what Nix declared. See [The cluster definition](../concepts/cluster-definition.md).

## Module group { #module-group }

A named set of kernel modules a role image carries, listed in
[`chalkos.kernel.moduleGroups`](options.md#chalkoskernelmodulegroups). The base groups
`storage`, `network`, `virtualisation`, `filesystems`, `kubernetes` and `platform` are on by
default; `gpu`, `sound`, `media`, `wireless`, `infiniband`, `can` and `industrial` are added per
role. See [The image](../concepts/image.md).

## Node { #node }

A machine of the cluster, declared under [`chalkos.nodes`](options.md#chalkosnodes) with a
[role](#role), a [platform](#platform) and its own settings, and installed from the image of its
role and platform. See [The cluster definition](../concepts/cluster-definition.md).

## Node CA { #node-ca }

An intermediate CA under the [OS CA](#os-ca) that may issue only server and client
certificates. Control planes hold it and renew every node's certificate with it, so they never
hold the OS CA's key. See [Certificates](../concepts/certificates.md).

## Node certificate { #node-certificate }

The certificate chalkd serves and authenticates with, issued by the [node CA](#node-ca) for the
node's name and addresses and valid for one year. chalkd renews it through a control plane once
two thirds of its lifetime have passed. See [Certificates](../concepts/certificates.md).

## Normal mode { #normal-mode }

The mode chalkd runs in on an installed node: it serves its node certificate and accepts only
clients with a certificate from the [OS CA](#os-ca). See
[Architecture](../concepts/architecture.md).

## Operator { #operator }

The [client role](#client-role) that may do what a [reader](#reader) may, and also reboot,
upgrade, drain and uncordon nodes. See [Security model](../concepts/security.md).

## OS CA { #os-ca }

The root certificate authority of a cluster's node API: chalkd and chalkctl trust only
certificates that chain to it. It lives in the [secrets file](#secrets-file), is valid for ten
years and signs the [node CA](#node-ca) and client certificates. See
[Certificates](../concepts/certificates.md).

## PCR 7 { #pcr-7 }

The TPM register that measures the Secure Boot state: whether it is on, the PK, KEK, db and dbx
contents, and the db certificate that verified the boot binaries. chalkos seals the keys of
encrypted partitions to it, so they unseal after an upgrade signed with the same key and not
after a change to Secure Boot. See [Security model](../concepts/security.md).

## Platform { #platform }

The kind of machine an image is built for, such as `metal` or `kvm`, defined as NixOS modules
under [`chalkos.platforms`](options.md#chalkosplatforms). Each role gets one image per platform,
so only the images for a platform carry its drivers and agents. See
[The cluster definition](../concepts/cluster-definition.md).

## Reader { #reader }

The [client role](#client-role) that may read: node information, disks, status, logs and etcd
members. See [Security model](../concepts/security.md).

## Recovery key { #recovery-key }

The second key of every encrypted partition, used when the TPM does not unseal. chalkos derives
it per node from the [secrets file](#secrets-file), so the file never changes, and
[`chalkctl recovery-key`](cli/chalkctl_recovery-key.md) prints it. See
[Storage and encryption](../concepts/storage.md).

## Role { #role }

A kind of node, declared under [`chalkos.roles`](options.md#chalkosroles) with its NixOS
modules, storage defaults and Kubernetes kind (`controlplane`, `worker` or none). Each role
becomes one [image](#image) per platform. See
[The cluster definition](../concepts/cluster-definition.md).

## Rollback { #rollback }

The return to the previous image after a new one used up its boot tries without a
[blessed boot](#blessed-boot). chalkd reports it in status with the failed boot's last log
lines, and `chalkctl upgrade` stops. See
[Boot, health and rollback](../concepts/boot-and-rollback.md).

## Root hash { #root-hash }

The top hash of the [store](#store)'s dm-verity hash tree. The UKI carries it on the kernel
command line (`usrhash=`), so the UKI's signature covers every byte of the store. See
[The image](../concepts/image.md).

## Rotation { #rotation }

The replacement of a CA or key with [`chalkctl rotate`](cli/chalkctl_rotate.md), in phases that
first trust the new value everywhere, then use it, then re-issue, and last remove the old value.
Rotation is also how access is revoked. See [Certificates](../concepts/certificates.md).

## Secrets file { #secrets-file }

The file [`chalkctl gen secrets`](cli/chalkctl_gen_secrets.md) writes once with every secret of
a cluster: the OS CA and node CA, the Kubernetes CAs and keys, and the secret recovery keys
derive from. It is `secrets.age`, encrypted with age, or `secrets.json`; its public part is
`secrets.pub.json`. See [Security model](../concepts/security.md).

## Secure Boot { #secure-boot }

The UEFI firmware's check that a boot binary is signed by a certificate in [db](#db-and-dbx).
chalkos signs systemd-boot and the [UKI](#uki), whose signature covers the store through the
[root hash](#root-hash). See [Security model](../concepts/security.md).

## Slot { #slot }

A pair of partitions that holds one image's [store](#store): the erofs data and its dm-verity
hash tree, labelled `store_<version>` and `store-verity_<version>`, or `_empty`. A node has two,
A and B. See [The image](../concepts/image.md).

## STATE { #state }

A small partition, 128 MiB by default, encrypted and sealed to the TPM by default. It holds the
node's identity, node certificate, trusted CAs, Kubernetes share and storage definitions, and
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
ESP as `chalkos_<version>.efi`. See [The image](../concepts/image.md).

## Upgrade { #upgrade }

The installation of a new image version into a node's inactive [slot](#slot), from which it
boots next with [boot counting](#boot-counting). [`chalkctl upgrade`](cli/chalkctl_upgrade.md)
rolls it through a cluster. A Kubernetes upgrade is an image upgrade. See
[Upgrades](../concepts/upgrades.md).

## VAR { #var }

The partition mounted at `/var` that holds what a node writes: etcd, containerd, the kubelet,
logs. It is declared in the node's storage, fills the rest of the system disk by default and is
encrypted like [STATE](#state). See [Storage and encryption](../concepts/storage.md).

## VIP { #vip }

A virtual IP address of the API server, one per IP family, that one healthy control plane
holds. chalkd elects the holder through etcd and announces the address on the local network.
See [Networking](../concepts/networking.md).

## Volume { #volume }

A partition beyond [VAR](#var) declared in a node's or role's storage, on the system disk or a
disk of its own, encrypted or not, formatted and mounted or left raw. See
[Storage and encryption](../concepts/storage.md).

## Worker { #worker }

A node whose role has the Kubernetes kind `worker`: it runs containerd and the kubelet, which
joins the cluster at the cluster endpoint with the certificate in its
[Kubernetes share](#kubernetes-share). See [Kubernetes on chalkos](../concepts/kubernetes.md).
