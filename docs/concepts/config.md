---
title: "Config and the inventory"
description: "How YAML config documents describe each node, how the inventory gives them out, and how a node applies them"
---

# Config and the inventory

A node's [config](../reference/glossary.md#config) is the set of YAML documents that makes it this
node: its name and type, the image it runs, its disks, network, time servers, Kubernetes settings
and the values of [extensions](../reference/glossary.md#extension). The image is the same on many
nodes; the config is what differs. An [inventory](../reference/glossary.md#inventory) names the
cluster's nodes, the address [chalkctl](../reference/glossary.md#chalkctl) reaches each at and the
files whose documents form each node's config. chalkctl checks the config against the node's image
and delivers it; [chalkd](../reference/glossary.md#chalkd) checks it again, keeps it on
[STATE](../reference/glossary.md#state) and applies it at every boot.

## What is a config document?

A [config document](../reference/glossary.md#config-document) is one object in the style of a
Kubernetes object: an `apiVersion` of a group and a version, a `kind`, a `metadata.name` and a
`spec`. The [kind](../reference/glossary.md#kind)'s JSON Schema, which the image carries, defines
the `spec`, its defaults and what it refuses, so a misspelled field is an error and not a value
that nothing reads. A file holds any number of documents, separated by `---`.

The lab template's shared file holds the documents every node of the lab gets:

```yaml title="cluster/cluster.yaml"
apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: lab}
spec:
  # cp1 on the lab network: the API server of the only control plane.
  endpoint: https://192.168.123.11:6443
---
apiVersion: chalkos.dev/v1alpha1
kind: Image
metadata: {name: node-kvm}
spec:
  # nix build .#node-kvm -o images/node-kvm puts the image here.
  source: {directory: ../images/node-kvm}
---
# Kubernetes with its defaults: IPv4, the pod range 10.244.0.0/16 and the service range
# 10.96.0.0/12.
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Kubernetes
metadata: {name: kubernetes}
spec: {}
```

Each node's own file names the node, the image it runs, its system disk and its address on the
lab network:

```yaml title="nodes/cp1.yaml"
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: cp1}
spec:
  type: controlplane
  imageRef: node-kvm
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

There is no layering of cluster and node values: a document is in a node's set or it is not.
A value shared by every node goes into a shared file once; a node that needs another value gets
its own document of that kind in its own file instead, and the shared file leaves the kind out.

## Which kinds are there?

chalkos's own kinds live in the group `chalkos.dev` and its subdomains, all at version
`v1alpha1`. [Config kinds](../reference/kinds.md) lists every field of each, generated from the
schemas.

| Kind | Group | Per node | What it holds |
| --- | --- | --- | --- |
| [`Cluster`](../reference/kinds.md#cluster) | `chalkos.dev` | exactly one | The cluster's name and the URL of its API server |
| [`Node`](../reference/kinds.md#node) | `chalkos.dev` | exactly one | The node's type, `controlplane` or `worker`, the `Image` it runs and its hostname |
| [`Image`](../reference/kinds.md#image) | `chalkos.dev` | one or more | The [image directory](../reference/glossary.md#image-directory) of a build |
| [`Secret`](../reference/kinds.md#secret) | `chalkos.dev` | any | Secret values by key, for the fields that reference them |
| [`SystemDisk`](../reference/kinds.md#systemdisk) | `storage.chalkos.dev` | exactly one | The system disk, the size of [VAR](../reference/glossary.md#var) and the encryption of the node's partitions |
| [`Volume`](../reference/kinds.md#volume) | `storage.chalkos.dev` | any | A further [volume](../reference/glossary.md#volume): its disk, size, format, mount point and encryption |
| [`TimeSync`](../reference/kinds.md#timesync) | `time.chalkos.dev` | at most one | Time servers, and whether those DHCP announces are used too |
| [`Network`](../reference/kinds.md#network), [`NetDev`](../reference/kinds.md#netdev), [`Link`](../reference/kinds.md#link) | `networkd.chalkos.dev` | any | systemd-networkd's `.network`, `.netdev` and `.link` files, one document per file |
| [`Kubernetes`](../reference/kinds.md#kubernetes) | `kubernetes.chalkos.dev` | exactly one on an image with Kubernetes | IP families, pod and service ranges, DNS addresses, how nodes pick their addresses, the [VIP](../reference/glossary.md#vip), scheduling on control planes and extra flags of the components |
| [`Kubelet`](../reference/kinds.md#kubelet) | `kubernetes.chalkos.dev` | at most one | The node's labels, taints and Kubernetes addresses |
| [`RegistryMirror`](../reference/kinds.md#registrymirror) | `kubernetes.chalkos.dev` | any | The mirrors of one container registry |
| [`Flannel`](../reference/kinds.md#flannel) | `kubernetes.chalkos.dev` | at most one, on images with flannel | flannel's MTU |
| [`Addon`](../reference/kinds.md#addon) | `kubernetes.chalkos.dev` | any | Kubernetes objects the control planes apply |

Some kinds depend on the image or the node:

- An image declares only the kinds its modules bring. `Kubernetes`, `Kubelet`, `RegistryMirror`
  and `Addon` exist on images with Kubernetes, and `Flannel` only on those built with flannel as
  their pod network, so a document of one of them is an unknown kind on any other image.
- A `Network`, `NetDev` or `Link` document's `metadata.name` is the unit file's name without its
  suffix, so `10-uplink` becomes `/run/systemd/network/10-uplink.network` and networkd's order by
  file name holds. Sections and keys are networkd's own in camelCase: `[Match]` with
  `MACAddress=` is `match: {macAddress: …}`. A section that networkd allows more than once, such as
  `[Route]`, is a list.
- An `Addon`'s objects go to the cluster's API, so chalkctl delivers `Addon` documents to control
  planes alone. A shared file may hold them; a worker's set leaves them out.

An extension adds kinds of its own in a group outside `chalkos.dev`, declared in the NixOS modules
of a [variant](../reference/glossary.md#variant), as
[Variants and platforms](variants.md#how-does-an-extension-add-a-kind) shows. Their documents go
into the same files as chalkos's. The homelab example's `Rack` kind records where a node is
mounted, and each node's file holds one:

```yaml
apiVersion: rack.example.org/v1alpha1
kind: Rack
metadata: {name: rack}
spec:
  location: rack-a/u12
```

## How does the inventory assign files to nodes?

The inventory is a YAML document of its own kind, `Inventory`, that chalkctl reads from
`chalkos.yaml` in the current directory or from the file `--inventory` names. It stays on the
operator's machine and never reaches a node. The lab template's:

```yaml title="chalkos.yaml"
# The lab's nodes, the address chalkctl reaches each at on the lab network, and their config
# files: every node gets cluster/, each its own file in nodes/. chalklab reaches the nodes at
# ports it forwards instead, through the inventory it writes into the lab's state directory.
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: lab}
spec:
  files: [cluster/*.yaml]
  nodes:
    cp1: {address: 192.168.123.11, files: [nodes/cp1.yaml]}
    w1: {address: 192.168.123.12, files: [nodes/w1.yaml]}
```

[Config kinds](../reference/kinds.md#inventory) lists its fields:

- `spec.files` are the files every node gets.
- `spec.groups.<group>.files` are the files of a group, which a node joins by naming the group in
  its `groups`.
- `spec.nodes.<node>` is each node by name, with its `address`, its `groups` and its own `files`.

A node's set is the documents of the shared files, then of its groups' files in the order the node
names the groups, then of its own files. Paths are relative to the inventory's directory, and a
glob such as `cluster/*.yaml` takes its files in name order. Every path or glob must name at least
one file, so a typo fails instead of leaving documents out. A file named twice counts once.

The order decides nothing about values, because documents never merge: two documents of the same
kind and name in one node's set are an error that names both files. A group is the place for what
a subset of nodes shares, such as the `Kubelet` labels of GPU workers:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  files: [cluster/*.yaml]
  groups:
    gpu: {files: [groups/gpu.yaml]}
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    w1: {address: 10.0.0.21, files: [nodes/w1.yaml]}
    w2: {address: 10.0.0.22, groups: [gpu], files: [nodes/w2.yaml]}
```

`address` is where chalkctl connects to the node's chalkd, as a host or `host:port`, also in
[maintenance mode](../reference/glossary.md#maintenance-mode) before the node has its config.
chalkctl never derives it from the node's `Network` documents, so a node that installs with a DHCP
address and receives a static one names the static one here; `--address` overrides it for one
command. Each node's `Node` document carries the name the inventory gives the node, and chalkctl
refuses a set whose `Node` is named otherwise.

The inventory names no secrets file. The commands that issue certificates or deliver config read
the [secrets file](../reference/glossary.md#secrets-file) from `--secrets`; the others
authenticate with a [client file](../reference/glossary.md#client-file).

## What makes a set valid?

A node's set is valid when:

- it holds exactly one `Cluster`, one `Node` and one `SystemDisk`, and the `Image` that the `Node`'s
  `spec.imageRef` names;
- every other kind appears at most once, unless the kind allows several (`Image`, `Secret`,
  `Volume`, `RegistryMirror`, `Addon`, `Network`, `NetDev`, `Link`, and extension kinds declared
  with `multiple = true`);
- every document's `apiVersion` and `kind` is one the node's image declares;
- no two documents of a kind share a `metadata.name`;
- every `secretRef` names a `Secret` in the set and a key that `Secret` holds;
- every document is valid against its kind's schema;
- on an image with Kubernetes, it holds one `Kubernetes` document; its defaults make `spec: {}` a
  valid one.

Beyond the schemas, chalkctl and chalkd translate the set as the node will use it, which checks
what a schema cannot express: overlapping ranges, a VIP outside the node's networks, a volume that
does not fit its disk, an extra flag that would override one chalkos sets. Every node of an
inventory must hold a `Cluster` of the same name and endpoint, because the cluster's name and
endpoint come from it.

## How does an Image document name an image?

An `Image` document names the [image directory](../reference/glossary.md#image-directory) of one
build in `spec.source.directory`, relative to the file that holds the document; a symbolic link to
that file does not move the directory it resolves against. The directory is what `nix build` writes
for one variant on one platform, such as `images/node-kvm` in the lab template. Its
`metadata.json` names the image's variant, platform and [root hash](../reference/glossary.md#root-hash),
and its `schemas/` holds the schemas of the kinds the image accepts.

The root hash stays on the operator's machine. chalkctl reads it from `metadata.json` when it
needs it and never puts it into the config it delivers, so a new build of an image changes no
node's config, and an upgrade needs no `apply-config` after it. The `Node`'s `spec.imageRef` names
the `Image` by its `metadata.name`, so a shared file may hold the `Image` of every variant and each
node picks its own.

## How is config checked?

Config is checked twice, against the schemas of the image it is for, so an error shows before
anything reaches a node and a node still never stores config its image does not accept.

chalkctl checks first. [`chalkctl validate`](../reference/cli/chalkctl_validate.md) builds the set
of each node, or of the nodes it names, and checks it against the schemas in the image directory
its `Image` names. It reaches no node and needs neither a secrets file nor a client file, so it
runs in CI or before a commit:

```sh
chalkctl validate
```

It prints a line for each node whose config is valid and names each error, and it warns of a node
whose `SystemDisk` leaves STATE unencrypted. The commands that deliver config run the same check
first, each against the image the config is for:

- [`chalkctl install`](../reference/cli/chalkctl_install.md) against the image it installs: the
  image directory the `Image` names, or the one `--image` gives;
- [`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) against the image the node
  runs, whose schemas and [contract](../reference/glossary.md#contract) the node returns through the
  [Contract](../reference/api.md#method-contract) method, refusing an image of another
  [platform](../reference/glossary.md#platform) than the node's `Image`.

chalkd checks again when the config arrives, against the schemas of the image the set is for, and
refuses an invalid set as a whole. chalkd, not chalkctl, fills in the schemas' defaults, so the set
on STATE is complete while the files stay as short as their authors wrote them.

## How does config reach a node?

chalkctl delivers a node's config at install and with
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md):

```sh
chalkctl apply-config w1 --secrets secrets.age
```

Both need the secrets file, because the node's certificates and secrets travel in the same
request. A set replaces the node's previous set as a whole: a document left out of the files is
gone from the node after the next `apply-config`. chalkd writes the new set on STATE to a new
directory and renames a symbolic link to it over the old one, so a power cut leaves either the old
set or the new one, never a mix.

An upgrade carries no config. [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) sends the
image alone, and the node checks the config it holds against the schemas of the new image before
that image becomes its next boot, refusing an image that would not accept its config. Config that
needs a kind the new image brings is applied after the upgrade.

A running node refuses a change it cannot carry out live, and the error says what the change
needs instead:

- A `Node.spec.type` other than the one it was installed with, once the node holds its
  Kubernetes share: the node is reinstalled as the new type.
- A `Kubernetes` document that changes the cluster's IP families, pod or service ranges or DNS
  addresses once the cluster exists: they stay for the cluster's life.
- A storage change that would destroy data, such as a volume removed, moved to another disk,
  formatted or encrypted otherwise, or shrunk: the node refuses the whole set, and
  [`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) recreates such a volume.
  A new volume, a larger one or another mount point applies while the node runs.

A control plane's addresses are not refused, but they are pinned once it becomes an
[etcd member](../reference/glossary.md#etcd-member): every later boot waits for exactly those
addresses, so a `Network` change that drops one keeps the control plane from starting etcd.

## What does a node do with its config?

`chalkos-config.service` applies the stored set at every boot, before the network starts, and
chalkd applies a new set the same way after every `apply-config`:

- it writes the networkd units to `/run/systemd/network`, where they take precedence over the
  image's own;
- it writes the time servers for chrony and sets the hostname;
- it writes the storage section that the next boot's volumes follow, and the Kubernetes
  configuration chalkd and the kubelet read;
- it writes each document to `/run/chalkos/config/<group>/<Kind>/<name>.json`, except `Secret`
  and `Addon` documents;
- it writes, for each service that reads config, the values that the `secretRef` fields of its
  kinds name into the service's credentials.

A service declares the kinds it reads under
[`chalkos.consumers`](../reference/options.md#chalkosconsumers) in its image's modules. It reads
their documents as files below `/run/chalkos/config`, or with
[chalkcfg](../reference/glossary.md#chalkcfg), whose commands
[`chalkos.kinds.<Kind>.get`](../reference/options.md#chalkoskindsget) builds with a path the
image's evaluation checks:

```console
$ chalkcfg get Rack rack -o jsonpath='{.spec.location}'
rack-a/u12
```

chalkcfg runs on the node, from a consumer's unit or a privileged pod. It prints no `Secret` and no
`Addon` document: `Addon` documents stay in the stored set, readable by root alone, and only a
control plane's chalkd applies their objects.

When `apply-config` changes a document of a service's kinds, or a `Secret` one of them references,
chalkd restarts the service, unless the service sets
[`restartOnChange = false`](../reference/options.md#chalkosconsumersrestartonchange). It restarts a
service that runs or stays active after it ran, and starts a failed one again, since the new config
may be what it lacked; a service that never started or was stopped stays so. `apply-config` prints
the units it restarted. For chalkos's own renderings, chalkd reloads networkd after every new set,
reloads chrony's sources when the time servers change, and restarts the kubelet when the node's
Kubernetes settings change.

## How do secrets reach a service?

A `Secret` document holds secret values by key in `spec.data`:

```yaml title="nodes/cp1.secrets.yaml"
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: bgp}
spec:
  data:
    password: s3cret
```

A field of an extension kind declared with the type `chalkosLib.types.secretRef` references one of
its keys instead of holding the value, written as
`password: {secretRef: {name: bgp, key: password}}`. chalkctl and chalkd refuse a set in which a
reference names a `Secret` or key the set lacks.

On the node, chalkd keeps `Secret` documents in a directory of STATE readable by root alone and
never writes them below `/run/chalkos/config`. A service that reads a kind with a reference gets
the value as its systemd credential `secret_<secret>.<key>`, here `secret_bgp.password`, and no
other service sees it. Status, chalkcfg and the config's hash never show a secret value.

## How does status compare a node with its config?

[`chalkctl status`](../reference/cli/chalkctl_status.md) compares a node with what the inventory
gives it, in two ways:

- The config: chalkctl hashes the documents it would deliver with SHA-256, in a canonical form in
  which each `Secret` value is replaced by its own hash, and compares the result with the hash
  the node reports. A node that differs shows
  `not the inventory's; chalkctl apply-config <node> delivers it`.
- The image: chalkctl compares the root hash of the store the node booted with the root hash in
  the `metadata.json` of the directory its `Image` names, and names both when they differ, or
  says when the image is installed and waits for the node's next boot.

The hash covers the documents as the files give them, before chalkd fills in defaults, so a new
image with other defaults does not change it.

## Limits

- chalkctl reads every file of a node's set as plain YAML, `Secret` documents included. A file of
  `Secret`s must be in plain text where chalkctl runs: kept out of version control, or decrypted
  into place by a tool such as sops before the command and removed after.
- Documents never merge and the inventory has no patches or templates, so two nodes whose `Kubelet`
  differs in one label each hold a whole `Kubelet` of their own.
- Config carries data, not code. A new service, or a change to how a service reads its value, is an
  image change and an [upgrade](upgrades.md).
- An upgrade carries no config, so config that needs a kind of the new image is applied after the
  upgrade, in a second step.
- A kind has one version per image, and there is no conversion between versions. A new version of
  a kind is a new set of documents.

## Related pages

- [Variants and platforms](variants.md) for the images that accept the config and how an extension
  declares a kind.
- [Architecture](architecture.md) for where the inventory and the image directories meet chalkctl.
- [Storage and encryption](storage.md) and [Networking](networking.md) for what the storage and
  network kinds do on the node.
- [Config kinds](../reference/kinds.md) for every field of every kind, the inventory's included.
- [`chalkctl validate`](../reference/cli/chalkctl_validate.md) and
  [`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) for the commands.
