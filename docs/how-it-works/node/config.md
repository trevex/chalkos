---
title: "Config and the inventory"
description: "How YAML config documents describe each node, how the inventory gives them out and how a node applies them"
---

# Config and the inventory

The lab template's node cp1 gets a file of its own, with its system disk and its address on the
lab network:

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
  disk: {path: /dev/vda}
---
# The card on the lab network, between the VMs. The other card reaches the internet and the
# ports chalklab forwards from 127.0.0.1, configured by DHCP.
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lab}
spec:
  match: {macAddress: "52:54:00:7b:00:11"}
  network: {address: [192.168.123.11/24]}
```

A node's config is the set of YAML documents that makes it this node: its host name, disks,
network, time servers and the values of extensions, and on the shipped variant its Kubernetes
settings. The image is the same on many nodes; the config is what differs. An inventory names the
cluster's nodes, the address chalkctl reaches each at, the image each runs and the files whose
documents form each node's config. chalkctl checks the config against the node's image and
delivers it. chalkd checks it again, keeps it on STATE for the image it is for and applies it at
every boot of that image.

## A config document is an object of one kind

A config document is one object in the style of a Kubernetes object: an `apiVersion` of a group and
a version, a `kind`, a `metadata.name` and a `spec`. The kind's JSON Schema, which the image
carries, defines the `spec`, its defaults and what it refuses, so a misspelled field is an error
and not a value that nothing reads. A file holds any number of documents, separated by `---`.

The lab template's shared file holds the documents every node of the lab gets:

```yaml title="cluster/cluster.yaml"
# Kubernetes with its defaults: IPv4, the pod range 10.244.0.0/16 and the service range
# 10.96.0.0/12.
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec:
  # cp1 on the lab network: the API server of the only control plane.
  endpoint: https://192.168.123.11:6443
```

## chalkos's kinds live in the group chalkos.dev

chalkos's own kinds live in the group `chalkos.dev` and its subdomains, all at version
`v1alpha1`. [Config kinds](../../reference/kinds.md) lists every field of each, generated from the
schemas. Every image accepts these:

| Kind | Group | Per node | What it holds |
| --- | --- | --- | --- |
| `Host` | `chalkos.dev` | at most one | The node's hostname, which defaults to the node's name |
| `Secret` | `chalkos.dev` | any | Secret values by key, for the fields that reference them |
| `SystemDisk` | `storage.chalkos.dev` | exactly one | The system disk, the size of VAR and the encryption of the node's partitions |
| `Volume` | `storage.chalkos.dev` | any | A further volume: its disk, size, format, mount point and encryption |
| `TimeSync` | `time.chalkos.dev` | at most one | Time servers, and whether those DHCP announces are used too |
| `Network`, `NetDev`, `Link` | `networkd.chalkos.dev` | any | systemd-networkd's `.network`, `.netdev` and `.link` files, one document per file |
| `Registry` | `oci.chalkos.dev` | any | Credentials of one registry or mirror host, and whether chalkd and containerd reach that host over plain HTTP |
| `Mirror` | `oci.chalkos.dev` | any | The mirrors of one registry |

The Per node column follows from each kind's `multiple`. A kind declared with `multiple = true`
lets a node hold several documents of it, each with a name of its own, such as one `Volume` per
volume. Without it, a node holds at most one, and chalkctl and chalkd refuse a second.

`Registry` and `Mirror` exist on every image, because chalkd reads them for the pull of an
install's or an upgrade's image, and containerd's mirror configuration is written from them. A
`Registry` gives a host's credentials and allows plain HTTP to it. A `Mirror` names the endpoints
chalkd and containerd try before a registry itself. The `Network`, `NetDev` and `Link` documents
mirror networkd's units one to one, as
[Network documents map one to one onto networkd units](networking.md#network-documents-map-one-to-one-onto-networkd-units)
describes.

!!! note "On the shipped variant, four kinds describe the cluster"

    Images with Kubernetes also accept the kinds of the group `kubernetes.chalkos.dev`. A node holds
    exactly one `Cluster`, with the cluster's endpoint, families, ranges and VIP, and exactly one
    `Node`, with its type, `controlplane` or `worker`, and its own Kubernetes settings. Images built
    with flannel accept at most one `Flannel`, and any number of `Addon` documents hold objects the
    control planes apply. chalkctl delivers `Addon` documents to control planes alone; a shared
    file may hold them, and a worker's set leaves them out. On any other image these kinds are
    unknown, and a node without Kubernetes has no type. The kubelet also takes a `Registry`'s
    credentials for every pod's pulls from its host.
    [Four kinds of document describe the cluster](../kubernetes/overview.md#four-kinds-of-document-describe-the-cluster)
    explains them.

An image declares only the kinds its modules bring. An extension adds kinds of its own in a group
outside `chalkos.dev`, declared in the NixOS modules of a variant, as
[Declare a config kind](../../build/declare-a-kind.md) shows. Their documents go into the same files
as chalkos's. The homelab example's `Rack` kind records where a node is mounted, and each node's
file holds one:

```yaml
apiVersion: rack.example.org/v1alpha1
kind: Rack
metadata: {name: rack}
spec:
  location: rack-a/u12
```

## The inventory gives each node a set of files

The inventory is a YAML document of its own kind, [`Inventory`](../../reference/kinds.md#inventory),
that chalkctl reads from `chalkos.yaml` in the current directory or from the file `--inventory`
names. It stays on the operator's machine and never reaches a node. The lab template's inventory
reads:

```yaml title="chalkos.yaml"
# The lab's nodes, the address chalkctl reaches each at on the lab network, the image they run
# and their config files: every node gets cluster/, each its own file in nodes/. chalklab reaches
# the nodes at ports it forwards instead, through the inventory it writes into the lab's state
# directory.
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: lab}
spec:
  images:
    # nix build .#node-kvm -o images/node-kvm puts the image here.
    node-kvm: {source: {directory: images/node-kvm}}
  all: {imageName: node-kvm, files: [cluster/*.yaml]}
  nodes:
    cp1: {address: 192.168.123.11, files: [nodes/cp1.yaml]}
    w1: {address: 192.168.123.12, files: [nodes/w1.yaml]}
```

Its `spec.all` is what every node gets, `spec.groups` what the nodes of each group get, and
`spec.nodes` each node by its name with the address chalkctl reaches it at and its own files.
[Write a node's config](../../operate/node-config.md) walks through the fields, with a group, and
[Config kinds](../../reference/kinds.md#inventory) lists them.

A node's set is the documents of the files of `spec.all`, then of its groups' files in the order
the node names the groups, then of its own files. Paths are relative to the inventory's directory,
and a glob such as `cluster/*.yaml` takes its files in name order. Every path or glob must name at
least one file, so a typo fails instead of leaving documents out. A file named twice counts once.

The order decides nothing about values, because documents never merge: a document is in a node's
set or it is not, and two documents of the same kind and name in one node's set are an error that
names both files. A value shared by every node goes into a shared file once. A node that needs
another value gets its own document of that kind in its own file, and the shared file leaves the
kind out. A group is the place for what a subset of nodes shares, such as the `Node` of GPU workers
with their type and labels.

The inventory names no secrets file and no client file. chalkctl reads them from its flags and its
environment, in the order
[Where chalkctl finds its credentials](../../reference/access.md#where-chalkctl-finds-its-credentials)
lists.

## A node's name is its key in the inventory

A node's name is its key in the inventory's `spec.nodes`, such as `cp1`. At install, chalkctl issues
the node's node certificate with the name as its subject's common name. chalkd reads the name from
that certificate whenever it needs it and has no other source for it: no config document names the
node, and a kind a node holds once takes any `metadata.name`, which the examples set to `default`.

The name feeds:

- the hostname, unless a [`Host`](../../reference/kinds.md#host) document's `hostname` gives
  another;
- the seed of the node's partition UUIDs, as [Storage and encryption](storage.md) describes;
- the node's recovery key, derived from the secrets file and the name;
- the `node.name` of the [change rules](../../build/guard-changes.md), the effects and the renderers
  of outputs;
- on the shipped variant, the Kubernetes node name, by which the kubelet registers and the node's
  etcd member is called.

A node keeps its name for its whole life. Renaming one takes a reinstall, because its certificates
and its recovery key carry the old name, and so does its etcd member.

The inventory's own `metadata.name` is a label and feeds nothing on a node. chalkctl names the
cluster by it in its messages, and [`chalkctl kubeconfig`](../../reference/cli/chalkctl_kubeconfig.md)
names the kubeconfig's cluster, user and context after it unless `--context` gives another name.
Two clusters may share it; their nodes still accept only their own cluster's certificates.

## A valid set holds each kind as often as the kind allows

A node's set is valid when:

- it holds exactly one `SystemDisk`, and on an image with Kubernetes one `Cluster`, which names the
  cluster's `endpoint` and whose defaults fill in its other fields, and one `Node`;
- every other kind appears at most once, unless the kind allows several (`Secret`, `Volume`,
  `Addon`, `Network`, `NetDev`, `Link` and extension kinds declared with `multiple = true`);
- every document's `apiVersion` and `kind` is one the node's image declares;
- no two documents of a kind share a `metadata.name`;
- every `secretRef` names a `Secret` in the set and a key that `Secret` holds;
- every document is valid against its kind's schema.

Beyond the schemas, chalkctl and chalkd translate the set as the node will use it, which checks
what a schema cannot express: overlapping ranges, a VIP outside the node's networks, a volume that
does not fit its disk, an extra flag that would override one chalkos sets.

!!! note "On the shipped variant, every node holds the same Cluster and Flannel specs"

    Each node builds the cluster from its own documents, so chalkctl refuses an inventory whose
    nodes hold different `Cluster` or `Flannel` specs, naming the first field that differs.
    [Every node holds the same cluster documents](../kubernetes/overview.md#every-node-holds-the-same-cluster-documents)
    explains the comparison.

## The inventory names the image each node runs

The inventory's `spec.images` names each image the nodes run and the image directory of its build
in `source.directory`, relative to the inventory's directory. The directory is what `nix build`
writes for one variant on one platform, such as `images/node-kvm` in the lab template. Where nodes
enforce Secure Boot, it is the copy of the build that
[`chalkbuild sign`](../../reference/cli/chalkbuild_sign.md) signed. Its `metadata.json` names the
image's variant, platform and root hash, and its `schemas/` holds the schemas of the kinds the
image accepts.

An image published to a registry is named by its reference in `source.oci` instead, as
[`chalkbuild push`](../../reference/cli/chalkbuild_push.md) prints it. Each image sets exactly one of
`directory` and `oci`. A reference by tag is resolved to a digest once per command, so one command
reads one image. `chalkctl validate`, `apply-config` and `status` read the image's header and
contract from the registry. `chalkctl install` on a node that runs the installer and
`chalkctl upgrade` each have the node pull the image by its digest, with the credentials of the
node's [`Registry`](../../reference/kinds.md#registry) for the registry's host.

```yaml
node-metal: {source: {oci: ghcr.io/example/node-metal@sha256:<digest>}}
```

[Publish images to a registry](../../build/publish-images.md) builds, signs and pushes such an image.

A node runs the image of its own `imageName`, else that of its groups, else `spec.all.imageName`.
chalkctl refuses an inventory that leaves a node without an image, or whose groups give a node two
different images.
[Groups share files and an image among some nodes](../../operate/node-config.md#groups-share-files-and-an-image-among-some-nodes)
shows an inventory whose GPU workers run an image of their own.

The image stays out of the node's config. chalkctl reads the root hash from `metadata.json` when it
needs it and never puts it, or the image's name, into the config it delivers. A new build of an
image or a node's move to another image therefore changes no node's config, and an upgrade needs
no `apply-config` after it.

## chalkctl and chalkd both check the config

Config is checked twice, against the schemas of the image it is for, so an error shows before
anything reaches a node and a node still never stores config its image does not accept.

chalkctl checks first. [`chalkctl validate`](../../reference/cli/chalkctl_validate.md) builds the set
of each node, or of the nodes it names, and checks it against the schemas in the directory of the
node's image in the inventory. It reaches no node and needs neither a secrets file nor a client
file, so it runs in CI or before a commit:

```sh
chalkctl validate
```

It prints a line for each node whose config is valid and names each error, and it warns of a node
whose `SystemDisk` leaves STATE unencrypted. For the lab template:

```text
cp1: valid for the image node-kvm (node on kvm, root hash 1044caccb099), config 7080d0996b95ef02999ede113b5fe02895fc1a3b87749fb5ad4a3ab28a105986
w1: valid for the image node-kvm (node on kvm, root hash 1044caccb099), config 0505d3467f8f64576cd78a10ed1c82f904e09f9229d588f402f630d2458f3861
```

Each line names the image's variant and platform, the start of its root hash, and the hash of the
node's config that [`chalkctl status`](../../reference/cli/chalkctl_status.md) compares. The commands
that deliver config run the same check first, each against the image the config is for:

- [`chalkctl install`](../../reference/cli/chalkctl_install.md) against the image it installs: the
  directory of the node's image in the inventory, or the one `--image` gives;
- [`chalkctl apply-config`](../../reference/cli/chalkctl_apply-config.md) against the image the node
  runs, whose schemas and contract the node returns through the
  [Contract](../../reference/api.md#method-contract) method, refusing an image of another
  platform than the node's image in the inventory. With `--staged`, against the node's image in
  the inventory, which the staged image is;
- [`chalkctl upgrade`](../../reference/cli/chalkctl_upgrade.md) against the image each node gets,
  before it drains any node.

chalkd checks again when the config arrives, against the schemas of the image the set is for, and
refuses an invalid set as a whole. chalkd, not chalkctl, fills in the schemas' defaults in the set
it keeps, so the slot on STATE holds the complete set while the files stay as short as their
authors wrote them.

Both also check the image's [change rules](../../build/guard-changes.md). chalkd runs every rule.
chalkctl runs those that read no facts about the node, `validate` without the node's config and
`apply-config` with the config the node serves, so a change a rule refuses fails before it is sent.

## chalkctl delivers a whole set at a time

chalkctl delivers a node's config at install, with each [upgrade](upgrades.md) and with
[`chalkctl apply-config`](../../reference/cli/chalkctl_apply-config.md):

```sh
chalkctl apply-config w1 --secrets secrets.age
```

Install and `apply-config` need the secrets file, because the node's certificates and secrets
travel in the same request. A set replaces the set the node keeps for the image as a whole: a
document left out of the files is gone from the node after the next `apply-config`.

Before it delivers a set, chalkctl asks the node for the set it keeps, through the
[GetConfig](../../reference/api.md#method-getconfig) method, and prints one line of what the delivery
changes:

```text
w1: config 1 added, 1 removed (Volume "scratch"), 2 changed
```

The line names every removed document, so a document dropped from the files shows before the node
loses it. `chalkctl apply-config --diff` and `chalkctl upgrade --diff` print the whole comparison
and deliver nothing: each document added (`+`), removed (`-`) or changed (`~`), and for a changed
one each field by its path, with the old and the new value. A secret field shows only as
`changed`. The node returns each value of a `Secret`, and each value of a Kubernetes `Secret` in
an `Addon`, as its SHA-256, and chalkctl compares the inventory's values by the same hashes.

## A node keeps one config slot per image

A node keeps a config slot on STATE for each image it holds, `/state/config/<root-hash>`, named by
the root hash of the image's store. A slot holds the documents as they were delivered, the same
documents with the schemas' defaults filled in, the config's version and the node's identity,
translated with that image's schemas. chalkd writes a slot to a new directory and renames a
symbolic link to it over the old one, so a power cut leaves either the old slot or the new one,
documents and identity together, never a mix.

At every boot, `chalkos-config.service` loads the slot of the image the kernel command line's
`usrhash=` names. The slots follow the images:

- An install writes the slot of the image it installs.
- An upgrade writes the slot of the new image. The image before keeps its own, so a
  [fallback](boot-and-rollback.md) boots it with the config it ran.
- `apply-config` writes the slot of the running image, and `apply-config --staged` the slot of the
  image an upgrade with `--no-reboot` staged to boot next. Nothing restarts for the staged slot.
- An upgrade removes the slot of every image no UKI on the ESP boots any more, so a node holds at
  most two slots, one for each of its images.

An image whose slot is missing boots without config: `chalkos-config` fails, the health check
fails with it, and a counted boot falls back. `chalkctl apply-config` on the running image warns
when an image is staged, whose slot keeps its own config:

```text
w1 has a staged image with its own config (version 6f0e…); apply-config --staged updates it
```

## Change rules decide which changes a node accepts

The config is the image's; the machine state is the node's. The machine state is what a node keeps
outside the slots, once for all its images: `storage.json`, the storage as applied, with the disks
and partitions it is pinned to, and the node certificate and the OS CAs it trusts. Every write of a
slot (install, upgrade, `apply-config` and `apply-config --staged`) checks its config against the
[change rules](../../build/guard-changes.md) of the image the slot is for. The rules compare it with
the running image's config and read the machine state from facts. The built-in storage kinds'
rules take:

| Change | Takes | What carries it out |
| --- | --- | --- |
| A new volume, a larger one, a larger VAR or another mount point | nothing | The write of the slot: the node makes the change at once. |
| A volume formatted or encrypted otherwise, or shrunk | reset | `chalkctl storage reset`, which recreates the volume and loses its data. |
| A volume removed or moved to another disk, or a change that would recreate VAR | reinstall | A reinstall of the node. |

A node refuses the whole set when one of its rules fails, applies none of it, and names what the
change needs, as [Guard changes with rules and facts](../../build/guard-changes.md) shows.

!!! note "On the shipped variant, the Kubernetes share fixes a node's type and the cluster's ranges"

    The Kubernetes share is machine state too. Once a node holds it, the rules refuse a
    `Node.spec.type` other than the one the node was installed with, which takes a reinstall as the
    new type. They also refuse a change of the cluster's IP families, pod or service ranges or DNS
    addresses, which stay for the cluster's life. The cluster file
    `/run/chalkos/kubernetes/cluster.json` is not machine state: `chalkos-config` writes it from the
    slot of the image that boots, and chalkd writes it again when `apply-config` changes the running
    image's config. A control plane's addresses are not refused, but they are pinned once it
    becomes an etcd member, as
    [A control plane's addresses are pinned once it joins etcd](../kubernetes/networking.md#a-control-planes-addresses-are-pinned-once-it-joins-etcd)
    explains.

The changes that take nothing are made whichever image the slot is for. An upgrade or
`apply-config --staged` that adds a volume creates it while the running image runs, enrolled with
the fallback secret chalkctl sends, and `storage.json` gains it. Every boot sets up the volumes
`storage.json` records, so the running image mounts the new volume as well, also after a fallback,
although its config does not name it. Nothing at boot removes a volume. A slot whose config lacks
the volume counts as removing it only when that slot is written again, so the next config for that
image must name the volume too.

That makes the order of two deliveries matter while an image is staged. A config for the running
image that adds a volume the staged image's config lacks would leave the staged image a slot that
counts as removing the volume, and the next write of that slot would be refused. So the node
refuses the config and names the first step: deliver the config to the staged image with
`chalkctl apply-config --staged <node>`, which creates the volume, then to the running image.

The check runs one way. A write of the running image's slot that grows the machine checks the
staged image's slot against the storage it makes. A write of the staged image's slot, by an
upgrade or `apply-config --staged`, does not check the running image's slot: the running image
mounts the new volume, and the next `apply-config` to it must name the volume.

## chalkos-config applies the slot at every boot

`chalkos-config.service` applies the running image's slot at every boot, before the network
starts, and chalkd applies a new set the same way after every `apply-config` to the running image:

- it sets the hostname;
- it writes each document to `/run/chalkos/config/<group>/<Kind>/<name>.json`, except `Secret`
  and `Addon` documents;
- it writes, for each unit granted kinds, the values that the `secretRef` fields of those kinds
  name into the unit's credentials;
- it renders the image's outputs, among them the networkd units in `/run/systemd/network` and the
  time servers for chrony, as [The config pipeline](config-pipeline.md) describes;
- on images with Kubernetes, it writes the Kubernetes configuration chalkd and the kubelet read.

The storage section is machine state: only chalkd writes `storage.json`, when a config that changes
the storage is delivered, and every boot sets up the volumes it records.

A service reads the node's config in one of two ways, which its image decides. A unit granted a
kind reads the documents of that kind below `/run/chalkos/config` or through chalkcfg, as
[Read config in a service](../../build/read-config.md) describes. An output renders the service's
own config files from the documents instead. An effect restarts or reloads the service when its
outputs or the documents of its kinds change, and `apply-config` prints the units it restarted.

chalkcfg runs on the node, from a granted unit or a privileged pod. It prints no `Secret` and no
`Addon` document: `Addon` documents stay in the stored set, readable by root alone, and only a
control plane's chalkd applies their objects.

## Secrets reach only the units granted their kind

A `Secret` document holds secret values by key in `spec.data`:

```yaml title="nodes/cp1.secrets.yaml"
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: bgp}
spec:
  data:
    password: s3cret
```

A field of an extension kind declared with the type `lib.chalkos.types.secretRef` references one of
its keys instead of holding the value, written as
`password: {secretRef: {name: bgp, key: password}}`. chalkctl and chalkd refuse a set in which a
reference names a `Secret` or key the set lacks.

On the node, chalkd keeps `Secret` documents in the config slot on STATE, readable by root alone,
and never writes them below `/run/chalkos/config`. A unit granted a kind with a reference gets the
value as a systemd credential, and no other service sees it, as
[Pass secrets to a service](../../build/pass-secrets.md) describes. Status, chalkcfg and the
config's hash never show a secret value.

!!! note "On the shipped variant, a Registry's password reaches the kubelet"

    `chalkos-registry-credentials.service` writes a `Registry`'s password to a file only root
    reads, and the kubelet uses it for every pod's pulls from the `Registry`'s host.

## Status compares a node with the inventory

[`chalkctl status`](../../reference/cli/chalkctl_status.md) compares a node with what the inventory
gives it, in two ways:

- The config: chalkctl hashes the documents it would deliver with SHA-256, in a canonical form in
  which each `Secret` value is replaced by its own hash, and compares the result with the hash the
  node reports. A node that differs shows
  `not the inventory's; chalkctl apply-config <node> delivers it`. With an image staged, status
  compares the staged slot's config the same way, and one that differs shows
  `not the inventory's; chalkctl apply-config --staged <node> delivers it`.
- The image: chalkctl compares the root hash of the store the node booted with the root hash in
  the `metadata.json` of the directory of its image in the inventory. It names both when they
  differ, or says when the image is installed and waits for the node's next boot.

The hash covers the documents as the files give them, before chalkd fills in defaults, so a new
image with other defaults does not change it.

## Limits

- chalkctl reads every file of a node's set as plain YAML, `Secret` documents included. A file of
  `Secret`s must be in plain text where chalkctl runs: kept out of version control, or decrypted
  into place by a tool such as sops before the command and removed after.
- Documents never merge and the inventory has no patches or templates, so two nodes whose `Node`
  differs in one label each hold a whole `Node` of their own.
- Config carries data, not code. A new service, or a change to how a service reads its value, is
  an image change and an [upgrade](upgrades.md).
- A kind has one version per image, and there is no conversion between versions. A new version of
  a kind is a new set of documents.
- A rule compares a config with the running image's config alone, and chalkctl runs only the rules
  that read no facts; see
  [Guard changes with rules and facts](../../build/guard-changes.md#rules-compare-with-the-running-config-alone).

## Related pages

- [The config pipeline](config-pipeline.md) for how a delivery passes rules, outputs and effects.
- [Declare a config kind](../../build/declare-a-kind.md) for how an extension declares a kind, and
  [Variants and platforms](../../build/variants-and-platforms.md) for the images that accept the
  config.
- [Architecture](architecture.md) for where the inventory and the image directories meet chalkctl.
- [Guard changes with rules and facts](../../build/guard-changes.md) for how an image decides which
  changes a node accepts.
- [Storage and encryption](storage.md) and [Networking](networking.md) for what the storage and
  network kinds do on the node.
- [Config kinds](../../reference/kinds.md) for every field of every kind, the inventory's included.
- [`chalkctl validate`](../../reference/cli/chalkctl_validate.md) and
  [`chalkctl apply-config`](../../reference/cli/chalkctl_apply-config.md) for the commands.
- [Write a node's config](../../operate/node-config.md) for writing the inventory and the
  documents, and [Set up your workstation](../../operate/workstation.md) for the secrets file and
  the client files.
- The [quick start](../../get-started/quick-start.md) and
  [Install a node](../../operate/install.md) for config files in use.
