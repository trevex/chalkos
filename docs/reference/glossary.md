---
title: "Glossary"
description: "The terms of the chalkos documentation, each with a short definition and the page that explains it"
---

# Glossary

Each entry defines a term the documentation uses repeatedly and links to the page that explains
it. The first sentence of an entry is a short definition, which every page shows as a tooltip on
the term. Option, command and API names link to the generated reference.

## A/B { #a-b }

The update scheme of a node's disk, with two [slots](#slot), A and B, that each hold an
[image](#image). The node boots one slot while an [upgrade](#upgrade) writes the other, so the
image it ran before stays on disk for a [rollback](#rollback). See
[The image](../how-it-works/node/image.md).

## Admin { #admin }
<!-- abbr: none -->

The [client role](#client-role) that may call almost every method of the node API. It may call
every method but the one nodes renew their certificates with, which needs the role `node`:
install, config changes, volume resets, bootstrap, etcd membership changes and rotations among
them. See [Security model](../how-it-works/node/security.md).

## Blessed boot { #blessed-boot }

A boot that the [health check](#health-check) found healthy. After it,
`systemd-bless-boot` removes the boot counter from the [UKI](#uki)'s file name. A blessed image
is never rolled back. See [Boot, health and rollback](../how-it-works/node/boot-and-rollback.md).

## Boot counting { #boot-counting }

systemd-boot's count of the tries a newly installed image has left. The count is kept in its
UKI's file name (`chalkos_<generation>+3.efi`). Each boot that is not [blessed](#blessed-boot)
uses a try; with none left, systemd-boot boots the previous image. The number of tries is
[`chalkos.upgrade.bootTries`](options.md#chalkosupgradeboottries). See
[Boot, health and rollback](../how-it-works/node/boot-and-rollback.md).

## Bootstrap { #bootstrap }

The one-time start of a cluster's etcd on its first [control plane](#control-plane). It runs
with [`chalkctl bootstrap`](cli/chalkctl_bootstrap.md) and is refused on a node that is bootstrapped or holds etcd data, so a cluster is never initialised
twice; further control planes join on their own. See
[The control plane](../how-it-works/kubernetes/control-plane.md).

## chalkbuild { #chalkbuild }

The command that prepares the images Nix builds for a cluster's nodes. `chalkbuild sign` signs
a copy of an [image directory](#image-directory) with the Secure Boot [db](#db-and-dbx) key, on
the machine that holds the key, and checks the copy against the db certificate. It never talks
to nodes. See [its commands](cli/chalkbuild.md) and
[Sign images for Secure Boot](../build/sign-images.md).

## chalkcfg { #chalkcfg }

The command on every node that prints the node's [config documents](#config-document). It reads
them from `/run/chalkos/config` and prints them by kind and
name, as names, JSON, YAML, a jsonpath expression or a Go template. It has no network access and
never prints `Secret`s or `Addon`s. A variant's NixOS modules build its commands with
[`chalkos.kinds.<Kind>.get`](options.md#chalkoskindsget), which checks a path against the kind's
options when the image is evaluated. See [its commands](cli/chalkcfg.md).

## chalkctl { #chalkctl }

The command-line client of chalkos. It reads a cluster from an [inventory](#inventory) and the
[image directories](#image-directory) it names for the nodes, installs, upgrades and operates nodes
through [chalkd](#chalkd), and issues certificates from the [secrets file](#secrets-file). It never
builds or signs images or runs Nix. See [its commands](cli/chalkctl.md) and
[Architecture](../how-it-works/node/architecture.md).

## chalkd { #chalkd }

The agent on every node. It serves the [node API](api.md) on TCP port 50000 over mutual TLS,
installs and upgrades the node, applies its [config](#config), runs the Kubernetes control
plane and decides whether a boot is healthy. See [Architecture](../how-it-works/node/architecture.md).

## chalklab { #chalklab }

The command that runs a cluster's `kvm` nodes as QEMU virtual machines on one machine. Each
virtual machine has Secure Boot and a TPM, and chalklab installs and bootstraps them. See
[its commands](cli/chalklab.md) and the [quick start](../get-started/quick-start.md).

## Change rule { #change-rule }
<!-- abbr: change rules, Change rules -->

A CEL expression that a config must make true for the image to accept it. An image's schemas
declare it on a [kind](#kind) or on one of its fields. A rule that is false refuses the config,
or says it takes [`chalkctl storage reset`](cli/chalkctl_storage_reset.md) or a reinstall. See
[Guard changes with rules and facts](../build/guard-changes.md).

## Client file { #client-file }
<!-- abbr: client files, Client files -->

A JSON file that lets someone operate nodes without the [secrets file](#secrets-file). It holds a
client certificate of one [client role](#client-role), its key and the [OS CA](#os-ca)
certificate, and [`chalkctl config new`](cli/chalkctl_config_new.md) issues it. It names no cluster: its OS CA ties it to one,
and a node of another cluster fails the TLS handshake with it. See
[Set up your workstation](../operate/workstation.md) and
[Security model](../how-it-works/node/security.md).

## Client role { #client-role }
<!-- abbr: client roles, Client roles -->

The permission a client certificate grants on the node API: [reader](#reader),
[operator](#operator) or [admin](#admin). The role is taken from the certificate's Organization,
and each role is allowed what the roles before it are. A certificate issued through the
[node CA](#node-ca) has the role `node`, which may only renew its node certificate. See
[Security model](../how-it-works/node/security.md).

## Cluster endpoint { #cluster-endpoint }

The URL of the cluster's API server, which kubelets, kubeconfigs and joining control planes use.
It is `spec.endpoint` of the [`Cluster`](kinds.md#cluster) document. Nodes also renew their
[node certificates](#node-certificate) at port 50000 of its host, so it points at a control plane,
a [VIP](#vip) or a load balancer that forwards both ports. See
[Networking](../how-it-works/node/networking.md).

## Config { #config }
<!-- abbr: none -->

A node's configuration at runtime: a set of [config documents](#config-document). Each document
is of a [kind](#kind) its image accepts, such as `Node`,
`Network`, `SystemDisk`, `Cluster` or an extension's own. The [inventory](#inventory) names the
YAML files whose documents form a node's set. chalkctl checks the set against the schemas of the
node's image and delivers it at install and with
[`chalkctl apply-config`](cli/chalkctl_apply-config.md); chalkd checks it again, keeps it on
[STATE](#state) and applies it at every boot. A new set replaces the node's previous one as a
whole. Secrets travel as `Secret` documents and reach only the services that read the kinds
referencing them. See [Config and the inventory](../how-it-works/node/config.md).

## Config document { #config-document }
<!-- abbr: config documents, Config documents -->

One object of a node's [config](#config), written in YAML like a Kubernetes object. It has an `apiVersion` of a group and a version, a `kind`, a `metadata.name` and a
`spec`. The schema of its [kind](#kind) defines the `spec`; [Config kinds](kinds.md) lists the
fields of the built-in kinds. On the node, chalkd writes each document except `Secret`s and
`Addon`s to `/run/chalkos/config/<group>/<Kind>/<name>.json`. See
[Config and the inventory](../how-it-works/node/config.md#what-is-a-config-document).

## Config slot { #config-slot }
<!-- abbr: config slots, Config slots -->

The directory on STATE that holds a node's config for one image, named by the image's root hash.
It is `/state/config/<root-hash>` on [STATE](#state), and holds the documents as delivered, the
same documents with the schemas' defaults filled in, the config's version and the node's
[identity](#identity). Each image's boot loads its own slot, so a [rollback](#rollback) boots the
previous image with the config it ran. An install or an [upgrade](#upgrade) writes the slot of the
image it writes, and `chalkctl apply-config` that of the running image. An upgrade removes the
slots of images no UKI on the ESP boots any more, so a node holds at most two. See
[Config and the inventory](../how-it-works/node/config.md#where-does-a-node-keep-its-config).

## Contract { #contract }

What an image accepts as [config](#config): its variant, platform, kind schemas and what it fixes
about Kubernetes. The variant and platform are the ones the image was built for, and the schemas
are the JSON Schemas of its [kinds](#kind). The image carries the schemas in
`/etc/chalkos/schemas` and the Kubernetes facts in `/etc/chalkos/kubernetes/image.json`, and its
[image directory](#image-directory) has them as `schemas/` and `image.json`. chalkctl reads the
contract from the image directory when it validates config, installs from the
[installer](#installer) or upgrades a node, and from the running node with the
[Contract](api.md#method-contract) method when it applies config. On an upgrade, the node reads
it from the image it wrote. See [Image and partition layout](image-layout.md).

## Control plane { #control-plane }
<!-- abbr: control planes, Control planes -->

A node that runs etcd and the Kubernetes API server, controller-manager and scheduler. Its `Node`
document sets `spec.type: controlplane`. It runs those components as static pods that chalkd
renders, besides containerd and the kubelet, and it is the only kind of node chalkctl delivers
`Addon` documents to. See [The control plane](../how-it-works/kubernetes/control-plane.md).

## db and dbx { #db-and-dbx }

The UEFI firmware's Secure Boot databases: db lists what the firmware boots, dbx what it
refuses. Both hold certificates and hashes of binaries. A chalkos image boots under Secure Boot
when its signer's certificate is in db and nothing of it is in dbx. See
[Security model](../how-it-works/node/security.md).

## dm-verity { #dm-verity }

The kernel's block-level integrity check, which makes a modified block of the store fail to
read. Each block of the [store](#store) is hashed into a hash tree whose top is the
[root hash](#root-hash), and a block that does not match its hash fails to read. See
[The image](../how-it-works/node/image.md).

## Effect { #effect }
<!-- abbr: nolower, Effects, effects -->

An action on the node's services after a delivery is applied, such as a unit restart. An effect
restarts or reloads units, runs a command or acts on chalkd's loops, as
[`chalkos.effects`](options.md#chalkoseffects) declares it. It fires when one of its
[outputs](#output) or the documents of one of its kinds changed, or when its CEL rule, like a
[change rule](#change-rule), holds. See
[React to changes](../build/react-to-changes.md).

## ESP { #esp }

The EFI system partition, a FAT file system at the start of the system disk. It holds
systemd-boot and the [UKIs](#uki) of both [slots](#slot), and is 1 GiB by default. See
[Image and partition layout](image-layout.md).

## etcd member { #etcd-member }
<!-- abbr: etcd members -->

A control plane that belongs to the cluster's etcd. It is a learner while it catches up and a
voter after. A control plane pins its addresses on [STATE](#state) when it bootstraps, or before
it adds itself as a learner, and every later boot waits for exactly them;
[`chalkctl etcd leave`](cli/chalkctl_etcd_leave.md) undoes both. See [The control
plane](../how-it-works/kubernetes/control-plane.md).

## Extension { #extension }
<!-- abbr: extensions, Extensions -->

NixOS modules outside chalkos that add services to a variant's images and may declare config
kinds. An extension sits in a [variant](#variant)'s modules and declares its [kinds](#kind) under
[`chalkos.kinds`](options.md#chalkoskinds), whose documents a node's YAML files then carry. The
services read those documents through a [grant](#grant) or as [outputs](#output) rendered from
them, and [effects](#effect) restart them when they change. chalkos's own Kubernetes support
declares its kinds and effects the same way. See
[Declare a config kind](../build/declare-a-kind.md).

## Fact { #fact }
<!-- abbr: facts, Facts -->

Node state that a [change rule](#change-rule) reads. Facts are declared in
[`chalkos.facts`](options.md#chalkosfacts): the JSON of a file, such as the
[Kubernetes share](#kubernetes-share), or of a program of the image's store, which chalkd runs in
a sandbox. See
[Guard changes with rules and facts](../build/guard-changes.md#facts-give-a-rule-node-state).

## Fallback keyslot { #fallback-keyslot }

The second keyslot of every encrypted partition, which unlocks it when the [TPM](#tpm) does not.
It holds the node's [recovery key](#recovery-key) by default, or a password, or the partition has
none, as `spec.encryption.fallback` of the node's [`SystemDisk`](kinds.md#systemdisk) chooses.
See
[Storage and encryption](../how-it-works/node/storage.md).

## Generation { #generation }

The number in a UKI's file name that orders a node's boot entries, the highest first. A node
counts it itself: the file name of a [UKI](#uki) on the [ESP](#esp) is `chalkos_<generation>.efi`.
The image and an install write
generation 1; each [upgrade](#upgrade) writes the one above the highest on the ESP, so
systemd-boot, which orders a node's UKIs by file name, boots the image installed last first,
older or newer. See
[Boot, health and rollback](../how-it-works/node/boot-and-rollback.md#how-does-systemd-boot-choose-an-entry).

## Grant { #grant }
<!-- abbr: grants, Grants -->

The [config](#config) kinds a unit may read at runtime. A unit's grant is declared in
[`systemd.services.<unit>.chalkos.kinds`](options.md#systemdserviceschalkoskinds): chalkd writes
the documents of those kinds below `/run/chalkos/config` and the values their
`secretRef` fields name into the unit's credentials. A grant restarts nothing; an
[effect](#effect) does. See
[Read config in a service](../build/read-config.md).

## Health check { #health-check }

The check that decides whether a counted boot is healthy. `chalkos-health.service` runs it. chalkd must serve on every node, and on a node with Kubernetes the preparation must
have succeeded. Then a control plane that is an etcd member needs its local etcd member, its API
server and its kubelet healthy, and a worker its kubelet healthy and its Node registered. A node
without Kubernetes, or a control plane not bootstrapped yet, needs its config applied and no unit
of the boot failed. A boot that passes within
[`chalkos.upgrade.healthTimeout`](options.md#chalkosupgradehealthtimeout) is
[blessed](#blessed-boot). See [Boot, health and rollback](../how-it-works/node/boot-and-rollback.md).

## Identity { #identity }
<!-- abbr: none -->

The secret-free form that chalkd translates a node's [config](#config) into before applying it.
It holds the node's hostname, static addresses, labels, taints, storage and Kubernetes
addresses. chalkd keeps it on [STATE](#state) in each image's config slot, translated with
that image's schemas, and the image's boot applies it. `chalkctl validate` translates a set the
same way to check it. See [Image and partition layout](image-layout.md#files-on-state).

## Image { #image }
<!-- abbr: none -->

The whole operating system of a node as one unsigned raw disk image. It has the ESP and slot A
filled and is built by `chalkos.lib.mkImages` for one [variant](#variant) on one
[platform](#platform). Every node that runs it boots the same bytes; what differs between nodes
travels in their [config](#config). Its build output is an [image directory](#image-directory).
See [The image](../how-it-works/node/image.md).

## Image directory { #image-directory }
<!-- abbr: image directories, Image directories -->

The build output of an [image](#image): the raw disk image with its metadata and config schemas.
It is `images.<variant>-<platform>` of `chalkos.lib.mkImages` and holds the raw image,
systemd-repart's report of its partitions, the definitions of the system region,
`metadata.json`, which names the image's variant, platform, [root hash](#root-hash),
[provenance](#provenance) and kinds, the kinds' JSON Schemas in `schemas/` and, on an image with
Kubernetes, what it fixes about Kubernetes in `image.json`. The [inventory](#inventory) names it in
an image's `source.directory`, [chalkbuild](#chalkbuild) signs a copy of it, and chalkctl reads
it to validate config, install and upgrade.
See [Image and partition layout](image-layout.md#outputs-of-an-image-build).

## Installer { #installer }

An image that always boots into [maintenance mode](#maintenance-mode) and writes a node's image to
a disk of the machine. `chalkos.lib.mkImages` builds one, as a raw image and a hybrid ISO, with
[`installer.modules`](options.md#installermodules); it installs images of every variant and
platform. See [Architecture](../how-it-works/node/architecture.md).

## Inventory { #inventory }

The file that tells chalkctl about a cluster: its nodes, their addresses, images and config
files. It lists each node by name, the address chalkctl reaches it at, the [image](#image) it
runs, and the YAML files whose documents form its [config](#config), as files every node gets,
files of named groups and each node's own. The inventory names each image by its
[image directory](#image-directory), relative to the inventory's directory; a node runs its own
`imageName`, else that of its groups, else the inventory's `spec.all.imageName`. Every node that
holds a `Cluster` document holds the same `Cluster` and `Flannel` specs. chalkctl reads
`chalkos.yaml` in the current directory, or the file `--inventory` names. It stays on the
operator's machine and names no secrets. Its keys of `spec.nodes` are the [node names](#node-name);
its `metadata.name` is a label for messages and the default kubeconfig context. See
[Write a node's config](../operate/node-config.md),
[Config and the inventory](../how-it-works/node/config.md#how-does-the-inventory-assign-files-to-nodes)
and its fields in [Config kinds](kinds.md#inventory).

## Kind { #kind }
<!-- abbr: none -->

The type of a [config document](#config-document), named by its `kind` and its `apiVersion`'s
group and version. chalkos declares its own kinds in `chalkos.dev` and its subdomains; an [extension](#extension) declares more under
[`chalkos.kinds`](options.md#chalkoskinds) in a variant's NixOS modules, with a group of its own.
An image accepts the kinds its modules declare and refuses any other. A unit reads the documents
of the kinds it is [granted](#grant). See
[Config and the inventory](../how-it-works/node/config.md#which-kinds-are-there) and
[Config kinds](kinds.md).

## Kubernetes share { #kubernetes-share }
<!-- abbr: nolower -->

The Kubernetes secrets a node receives at install. On a control plane, these are the cluster,
front-proxy and etcd CAs with their keys, the service-account key, the encryption key and the
node CA; on a worker, the cluster CA certificate and a kubelet client certificate. chalkd keeps it
on [STATE](#state). See [Certificates](../how-it-works/node/certificates.md).

## Lab { #lab }
<!-- abbr: none -->

A cluster that [chalklab](#chalklab) runs as virtual machines, described by a lab file. The lab
file, `chalklab.yaml`, names the cluster's [inventory](#inventory) and each
VM's MAC address, memory, CPUs and disks. The lab's state (disks, firmware variables, TPM state,
keys, logs, kubeconfig, client file and an inventory that reaches the nodes at forwarded ports)
lives in `$XDG_STATE_HOME/chalklab/<name>`, after the lab file's `metadata.name`. See the
[quick start](../get-started/quick-start.md) and [Config kinds](kinds.md#lab).

## Maintenance mode { #maintenance-mode }

The mode chalkd runs in on a node that is not installed. In it, chalkd serves a self-signed
certificate, prints its fingerprint and the node's addresses on the console, and accepts only
reads, reboots and an install. See [Architecture](../how-it-works/node/architecture.md#maintenance-mode-serves-a-node-that-is-not-installed).

## Module group { #module-group }
<!-- abbr: module groups, Module groups -->

A named set of kernel modules an [image](#image) carries. An image's groups are listed in
[`chalkos.kernel.moduleGroups`](options.md#chalkoskernelmodulegroups). The base groups
`storage`, `network`, `virtualisation`, `filesystems`, `kubernetes` and `platform` are on by
default; `gpu`, `sound`, `media`, `wireless`, `infiniband`, `can` and `industrial` are added in a
variant's or platform's modules. See [The image](../how-it-works/node/image.md).

## Node { #node }
<!-- abbr: none -->

A machine of the cluster, named in the [inventory](#inventory). The inventory also names the
[image](#image) it runs. On an image with Kubernetes, its `Node` document of the group
`kubernetes.chalkos.dev` says whether it is a [control plane](#control-plane) or a
[worker](#worker), and gives its labels, taints and Kubernetes addresses. It is installed from
that image's [image directory](#image-directory). See
[Config and the inventory](../how-it-works/node/config.md).

## Node CA { #node-ca }

An intermediate CA under the [OS CA](#os-ca) that may issue only server and client certificates.
Control planes hold it and renew every node's certificate with it, so
they never hold the OS CA's key. See [Certificates](../how-it-works/node/certificates.md).

## Node certificate { #node-certificate }
<!-- abbr: node certificates, Node certificates -->

The certificate chalkd serves and authenticates with. The [node CA](#node-ca) issues it for the
node's name, host name and addresses, valid for one year. chalkd takes the node's name from its
subject and renews it through a control plane once two thirds of its lifetime have passed. See
[Certificates](../how-it-works/node/certificates.md).

## Node name { #node-name }
<!-- abbr: node names, Node names -->

A [node](#node)'s key in the [inventory](#inventory)'s `spec.nodes`, such as `cp1`. chalkctl puts it into the [node certificate](#node-certificate) at
install, and chalkd reads it from there. The hostname defaults to it unless a `Host` document
gives another, and the Kubernetes node name, the etcd member, the seed of the partition UUIDs and
the [recovery key](#recovery-key) derive from it, so renaming a node takes a reinstall. See
[Config and the inventory](../how-it-works/node/config.md#what-names-a-node).

## Normal mode { #normal-mode }

The mode chalkd runs in on an installed node. In it, chalkd serves its node certificate and
accepts only clients with a certificate from the [OS CA](#os-ca). See
[Architecture](../how-it-works/node/architecture.md#normal-mode-serves-an-installed-node).

## OCI image { #oci-image }

A chalkos image published to a registry as one OCI artifact. chalkctl reads its header and
contract from the registry when the [inventory](#inventory) names the image by reference, and
nodes pull it in an install from the [installer](#installer) or an [upgrade](#upgrade). See
[Publish images to a registry](../build/publish-images.md).

## Operator { #operator }
<!-- abbr: none -->

The [client role](#client-role) that may do what a [reader](#reader) may, and also reboot,
upgrade, drain and uncordon nodes. An upgrade that delivers config
needs an [admin](#admin). See [Security model](../how-it-works/node/security.md).

## OS CA { #os-ca }

The root certificate authority of a cluster's node API: chalkd and chalkctl trust only
certificates that chain to it. It lives in the [secrets file](#secrets-file), is valid for ten
years and signs the [node CA](#node-ca) and client certificates. See
[Certificates](../how-it-works/node/certificates.md).

## Output { #output }
<!-- abbr: none -->

A set of config files for a service, rendered from config documents. A program of the image's
store renders them, and they are checked before a delivery changes the node, as
[`chalkos.outputs`](options.md#chalkosoutputs) declares it. chalkd renders every output again at
boot. See
[Render config files](../build/render-config.md).

## PCR 7 { #pcr-7 }

The TPM register that measures the Secure Boot state. It measures whether Secure Boot is on, the
PK, KEK, db and dbx contents, and the db certificate that verified the boot binaries. chalkos
seals the keys of encrypted partitions to it, so they unseal after an upgrade signed with the
same key and not after a change to Secure Boot. See
[Security model](../how-it-works/node/security.md).

## Platform { #platform }
<!-- abbr: platforms, Platforms -->

The kind of machine an image is built for, such as `metal` or `kvm`. Each platform is named in
the [`platforms`](options.md#platforms) argument of `chalkos.lib.mkImages` with NixOS modules of
its own. Every [variant](#variant) gets one [image](#image) per platform, so only that platform's
images carry its drivers and agents. A node refuses an image of another platform than the one it
runs. See [Variants and platforms](../build/variants-and-platforms.md#a-platform-builds-an-image-per-kind-of-machine).

## Provenance { #provenance }

The git revision and tag an image was built from. `chalkos.lib.mkImages` takes them as `revision`
and `tag` and writes them into the [image](#image)'s `metadata.json`. chalkctl shows them beside
the [root hash](#root-hash), as in `3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)`, and a node keeps them in
`/state/chalkd/images.json`. They never reach the store, and nothing orders or refuses an image by
them. See [The image](../how-it-works/node/image.md#what-identifies-an-image).

## Reader { #reader }
<!-- abbr: none -->

The [client role](#client-role) that may read node information, disks, status, logs and etcd
members. See [Security model](../how-it-works/node/security.md).

## Recovery key { #recovery-key }
<!-- abbr: recovery keys, Recovery keys -->

The default [fallback keyslot](#fallback-keyslot) of every encrypted partition, used when the TPM
does not unseal. chalkos derives it from the
[secrets file](#secrets-file) and the [node name](#node-name), so the file never changes, and
[`chalkctl recovery-key`](cli/chalkctl_recovery-key.md) prints it. See
[Storage and encryption](../how-it-works/node/storage.md).

## Revoked node { #revoked-node }
<!-- abbr: revoked nodes, Revoked nodes -->

A node shut out of the cluster by name with [`chalkctl node revoke`](cli/chalkctl_node_revoke.md).
Control planes list it in the ConfigMap `kube-system/chalkos-revoked-nodes` and refuse to renew
its node certificate, and the API server denies every write by its kubelet. Its certificates stay valid
until they expire. [`chalkctl node unrevoke`](cli/chalkctl_node_unrevoke.md) lets the name back in. See
[Security model](../how-it-works/node/security.md#revoking-a-node-cuts-off-its-writes-and-renewals).

## Rollback { #rollback }

The return to the previous image after a new one used up its boot tries without a
[blessed boot](#blessed-boot). chalkd reports it in status
with the failed boot's last log lines, and `chalkctl upgrade` stops. See
[Boot, health and rollback](../how-it-works/node/boot-and-rollback.md).

## Root hash { #root-hash }

The top hash of the [store](#store)'s dm-verity hash tree. The UKI carries it on the kernel command line (`usrhash=`), so the UKI's signature covers
every byte of the store. It identifies an image: chalkd and chalkctl tell images apart by it
alone, and chalkctl shows its first 12 hex characters. See
[The image](../how-it-works/node/image.md#what-identifies-an-image).

## Rotation { #rotation }
<!-- abbr: rotations, Rotations -->

The replacement of a CA or key, in phases, with [`chalkctl rotate`](cli/chalkctl_rotate.md). The
phases first trust the new value everywhere, then use it,
then re-issue, and last remove the old value. Rotation also revokes what the old value issued.
See
[Certificates](../how-it-works/node/certificates.md).

## Secrets file { #secrets-file }

The file that holds every secret of a cluster, written once by
[`chalkctl gen secrets`](cli/chalkctl_gen_secrets.md). It holds the OS CA and node CA, the
Kubernetes CAs and keys, and the secret recovery keys derive from. It is `secrets.age`, encrypted
with age, or `secrets.json`; its public part is `secrets.pub.json`. chalkctl reads it only where
`--secrets` names it, or from standard input with `--secrets -`; an inventory names none. See
[Set up your workstation](../operate/workstation.md) and
[Security model](../how-it-works/node/security.md).

## Secure Boot { #secure-boot }

The UEFI firmware's check that a boot binary is signed by a certificate in [db](#db-and-dbx).
chalkos signs systemd-boot and the [UKI](#uki), whose signature
covers the store through the [root hash](#root-hash). See
[Security model](../how-it-works/node/security.md).

## Slot { #slot }
<!-- abbr: slots, Slots -->

A pair of partitions that holds one image's [store](#store): the erofs data and its dm-verity hash
tree. They are labelled `store_<hash16>` and `store-verity_<hash16>`
from the first 16 hex characters of its [root hash](#root-hash), or `_empty`. A node has two, A
and B. See [The image](../how-it-works/node/image.md).

## STATE { #state }

A small partition that holds a node's config, certificates and keys. It is 128 MiB, encrypted and
sealed to the TPM, all by default. It holds the node's config, node certificate,
trusted CAs, Kubernetes share and storage definitions, and the marker that the node is installed.
See [Storage and encryption](../how-it-works/node/storage.md).

## Store { #store }

The read-only Nix store of an image: an erofs file system compressed with zstd, checked by
dm-verity on every read. See [The image](../how-it-works/node/image.md).

## TPM { #tpm }

The machine's Trusted Platform Module 2.0, which seals disk keys to the Secure Boot state. It
seals the keys of [STATE](#state), [VAR](#var) and encrypted volumes to [PCR 7](#pcr-7), so they
unlock without a password only under the same Secure Boot state. See
[Security model](../how-it-works/node/security.md).

## UKI { #uki }
<!-- abbr: UKIs -->

A unified kernel image: the kernel, initrd, kernel command line and os-release in one EFI
binary. systemd-boot loads it and Secure Boot verifies it. chalkos keeps one per slot on the ESP
as `chalkos_<generation>.efi`. See [The image](../how-it-works/node/image.md).

## Upgrade { #upgrade }
<!-- abbr: none -->

The installation of another image into a node's inactive [slot](#slot), from which it boots next.
That boot uses [boot counting](#boot-counting).
[`chalkctl upgrade`](cli/chalkctl_upgrade.md) rolls it through a cluster. A Kubernetes upgrade is
an image upgrade. See [Upgrades](../how-it-works/node/upgrades.md).

## VAR { #var }

The partition mounted at `/var` that holds what a node writes. That includes etcd, containerd,
the kubelet and logs. The node's [`SystemDisk`](kinds.md#systemdisk) document sizes it; by
default it fills the rest of the system disk, and it is encrypted like [STATE](#state). See
[Storage and encryption](../how-it-works/node/storage.md).

## Variant { #variant }
<!-- abbr: variants, Variants -->

A named set of NixOS modules that `chalkos.lib.mkImages` builds into one [image](#image) per
[platform](#platform). Its images are `images.<variant>-<platform>`, and it is given in the [`variants`](options.md#variants) argument: for example one variant for most nodes
and one with GPU drivers. Control planes and workers can run the same variant. Its name is
`CHALKOS_VARIANT` in the image's os-release and `variant` in its `metadata.json`. No check
compares it: a node may change its variant through an [upgrade](#upgrade), and the new image's
schemas decide which [config](#config) it takes. See
[Variants and platforms](../build/variants-and-platforms.md#a-second-variant-builds-a-second-image).

## VIP { #vip }
<!-- abbr: VIPs -->

A virtual IP address of the API server, one per IP family, that one healthy control plane
holds. chalkd elects the holder through etcd and announces the address on the local network.
See [Networking](../how-it-works/node/networking.md).

## Volume { #volume }
<!-- abbr: volumes, Volumes -->

A partition beyond [VAR](#var), declared by a [`Volume`](kinds.md#volume) document in a node's
config. It sits on the system disk or a disk of its own,
encrypted or not, formatted and mounted or left raw. See
[Storage and encryption](../how-it-works/node/storage.md).

## Worker { #worker }
<!-- abbr: workers, Workers -->

A node that runs containerd and the kubelet but no control plane components. Its `Node` document
sets `spec.type: worker`. Its kubelet joins the cluster at the
[cluster endpoint](#cluster-endpoint) with the certificate in its
[Kubernetes share](#kubernetes-share). See
[How Kubernetes runs on a node](../how-it-works/kubernetes/overview.md).
