---
title: "Config and the inventory"
description: "How YAML config documents describe each node, how the inventory gives them out and how a node applies them"
---

# Config and the inventory

A node's [config](../reference/glossary.md#config) is the set of YAML documents that makes it this
node: its name and type, the image it runs, its disks, network, time servers, Kubernetes settings
and the values of [extensions](../reference/glossary.md#extension). The image is the same on many
nodes; the config is what differs. An [inventory](../reference/glossary.md#inventory) names the
cluster's nodes, the address [chalkctl](../reference/glossary.md#chalkctl) reaches each at and the
files whose documents form each node's config. chalkctl checks the config against the node's image
and delivers it; [chalkd](../reference/glossary.md#chalkd) checks it again, keeps it on
[STATE](../reference/glossary.md#state) for the image it is for and applies it at every boot of
that image.

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
| [`Kubernetes`](../reference/kinds.md#kubernetes) | `kubernetes.chalkos.dev` | exactly one on an image with Kubernetes | IP families, pod and service ranges, DNS addresses, how nodes pick their addresses, the [VIP](../reference/glossary.md#vip), scheduling on [control planes](../reference/glossary.md#control-plane) and extra flags of the components |
| [`Kubelet`](../reference/kinds.md#kubelet) | `kubernetes.chalkos.dev` | at most one | The node's labels, taints and Kubernetes addresses |
| [`RegistryMirror`](../reference/kinds.md#registrymirror) | `kubernetes.chalkos.dev` | any | The mirrors of one container registry |
| [`Flannel`](../reference/kinds.md#flannel) | `kubernetes.chalkos.dev` | at most one, on images with flannel | flannel's MTU |
| [`Addon`](../reference/kinds.md#addon) | `kubernetes.chalkos.dev` | any | Kubernetes objects the control planes apply |

The Per node column follows from each kind's `multiple`. A kind declared with `multiple = true`
lets a node hold several documents of it, each with a name of its own, such as one `Volume` per
volume; without it, a node holds at most one, and chalkctl and chalkd refuse a second.

Some kinds depend on the image or the node:

- An image declares only the kinds its modules bring. `Kubernetes`, `Kubelet`, `RegistryMirror`
  and `Addon` exist on images with Kubernetes, and `Flannel` only on those built with flannel as
  their pod network, so a document of one of them is an unknown kind on any other image.
- A `Network`, `NetDev` or `Link` document's `metadata.name` is the unit file's name without its
  suffix, so `10-uplink` becomes `/run/systemd/network/10-uplink.network` and networkd's order by
  file name holds. Sections and keys are networkd's own in camelCase: `[Match]` with `MACAddress=`
  is `match: {macAddress: …}`. A section that networkd allows more than once, such as `[Route]`,
  is a list.
- An `Addon`'s objects go to the cluster's API, so chalkctl delivers `Addon` documents to control
  planes alone. A shared file may hold them; a [worker](../reference/glossary.md#worker)'s set
  leaves them out.

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

The inventory is a YAML document of its own kind, [`Inventory`](../reference/kinds.md#inventory),
that chalkctl reads from `chalkos.yaml` in the current directory or from the file `--inventory`
names. It stays on the operator's machine and never reaches a node. The lab template's inventory
reads:

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
a subset of nodes shares, such as the `Kubelet` labels of GPU workers. This inventory extends the
homelab example's with a group `gpu` and a GPU worker `w2` in it:

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
the [secrets file](../reference/glossary.md#secrets-file) from `--secrets FILE`, or from standard
input with `--secrets -`. The others authenticate with the secrets file when `--secrets` is given,
and otherwise with a [client file](../reference/glossary.md#client-file): the one `--config`
names, else the one `$CHALKOSCONFIG` names, else `~/.config/chalkos/config`.

## What makes a set valid?

A node's set is valid when:

- it holds exactly one `Cluster`, one `Node` and one `SystemDisk`, and the `Image` that the `Node`'s
  `spec.imageRef` names;
- every other kind appears at most once, unless the kind allows several (`Image`, `Secret`,
  `Volume`, `RegistryMirror`, `Addon`, `Network`, `NetDev`, `Link` and extension kinds declared
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
inventory must hold a `Cluster` of the same name and endpoint, because each node takes the
cluster's name and endpoint from its own `Cluster`.

## How does an Image document name an image?

An `Image` document names the [image directory](../reference/glossary.md#image-directory) of one
build in `spec.source.directory`, relative to the file that holds the document; a symbolic link to
that file does not move the directory it resolves against. The directory is what `nix build` writes
for one variant on one platform, such as `images/node-kvm` in the lab template. Its `metadata.json`
names the image's variant, platform and [root hash](../reference/glossary.md#root-hash), and its
`schemas/` holds the schemas of the kinds the image accepts.

The root hash stays on the operator's machine. chalkctl reads it from `metadata.json` when it needs
it and never puts it into the config it delivers, so a new build of an image changes no node's
config, and an [upgrade](../reference/glossary.md#upgrade) needs no `apply-config` after it. The
`Node`'s `spec.imageRef` names the `Image` by its `metadata.name`, so a shared file may hold the
`Image` of every variant and each node picks its own.

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
whose `SystemDisk` leaves STATE unencrypted. For the lab template:

```text
cp1: valid for the Image node-kvm (node on kvm, root hash 1044caccb099), config 7080d0996b95ef02999ede113b5fe02895fc1a3b87749fb5ad4a3ab28a105986
w1: valid for the Image node-kvm (node on kvm, root hash 1044caccb099), config 0505d3467f8f64576cd78a10ed1c82f904e09f9229d588f402f630d2458f3861
```

Each line names the image's variant and platform, the start of its root hash, and the hash of the
node's config that [`chalkctl status`](../reference/cli/chalkctl_status.md) compares. The commands
that deliver config run the same check first, each against the image the config is for:

- [`chalkctl install`](../reference/cli/chalkctl_install.md) against the image it installs: the
  image directory the `Image` names, or the one `--image` gives;
- [`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) against the image the node
  runs, whose schemas and [contract](../reference/glossary.md#contract) the node returns through the
  [Contract](../reference/api.md#method-contract) method, refusing an image of another
  [platform](../reference/glossary.md#platform) than the node's `Image`. With `--staged`, against
  the image the node's `Image` names, which the staged image is;
- [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) against the image each node gets,
  before it drains any node.

chalkd checks again when the config arrives, against the schemas of the image the set is for, and
refuses an invalid set as a whole. chalkd, not chalkctl, fills in the schemas' defaults in the set
it keeps, so the slot on STATE holds the complete set while the files stay as short as their
authors wrote them.

Both also check the image's [change rules](rules.md). chalkd runs every rule; chalkctl runs those
that read no facts about the node, `validate` without the node's config and `apply-config` with
the config the node serves, so a change a rule refuses fails before it is sent.

## How does config reach a node?

chalkctl delivers a node's config at install, with each [upgrade](upgrades.md) and with
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md):

```sh
chalkctl apply-config w1 --secrets secrets.age
```

Install and `apply-config` need the secrets file, because the node's certificates and secrets
travel in the same request. A set replaces the set the node keeps for the image as a whole: a
document left out of the files is gone from the node after the next `apply-config`.

Before it delivers a set, chalkctl asks the node for the set it keeps, through the
[GetConfig](../reference/api.md#method-getconfig) method, and prints one line of what the delivery
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

## Where does a node keep its config?

A node keeps a config slot on STATE for each image it holds, `/state/config/<root-hash>`, named by
the [root hash](../reference/glossary.md#root-hash) of the image's store. A slot holds the
documents as they were delivered, the same documents with the schemas' defaults filled in, the
config's version and the node's [identity](../reference/glossary.md#identity), translated with
that image's schemas. chalkd writes a slot to a new directory and renames a symbolic link to it
over the old one, so a power cut leaves either the old slot or the new one, documents and identity
together, never a mix.

At every boot, `chalkos-config.service` loads the slot of the image the kernel command line's
`usrhash=` names. The slots follow the images:

- An install writes the slot of the image it installs.
- An upgrade writes the slot of the new image. The image before keeps its own, so a
  [fallback](boot-and-rollback.md) boots it with the config it ran.
- `apply-config` writes the slot of the running image, and `apply-config --staged` the slot of the
  image an upgrade with `--no-reboot` staged to boot next. Nothing restarts for the staged slot.
- An upgrade removes the slot of every image no UKI on the ESP boots any more, so a node holds at
  most two slots, one for each of its images.

An image whose slot is missing boots without config: `chalkos-config` fails, the
[health check](../reference/glossary.md#health-check) fails with it, and a counted boot falls back.
`chalkctl apply-config` on the running image warns when an image is staged, whose slot keeps its
own config:

```text
w1 has a staged image with its own config (version 6f0e…); apply-config --staged updates it
```

## Which changes does a node accept?

The config is the image's; the machine state is the node's. The machine state is what a node keeps
outside the slots, once for all its images: `storage.json`, the storage as applied, with the disks
and partitions it is pinned to; the Kubernetes share; and the node certificate and the OS CAs it
trusts. The Kubernetes cluster file, `/run/chalkos/kubernetes/cluster.json`, is not machine state:
`chalkos-config` writes it from the slot of the image that boots, and chalkd writes it again when
`apply-config` changes the running image's config. Every write of a slot (install, upgrade,
`apply-config` and `apply-config --staged`) checks its config against the
[change rules](rules.md) of the image the slot is for, which compare it with the running image's
config and read the machine state from facts. The built-in kinds' rules take:

| Change | Takes | What carries it out |
| --- | --- | --- |
| A new volume, a larger one, a larger VAR or another mount point | nothing | The write of the slot: the node makes the change at once. |
| A volume formatted or encrypted otherwise, or shrunk | reset | [`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md), which recreates the volume and loses its data. |
| A volume removed or moved to another disk, or a change that would recreate VAR | reinstall | A reinstall of the node. |
| A `Node.spec.type` other than the one the node was installed with, once it holds its Kubernetes share | refuse | A reinstall of the node as the new type. |
| The cluster's IP families, pod or service ranges or DNS addresses, once the node holds its Kubernetes share | refuse | Nothing: they stay for the cluster's life. |

A node refuses the whole set when one of its rules fails, applies none of it, and names what the
change needs, as [Change rules and facts](rules.md) shows. The changes that take nothing are made
whichever image the slot is for: an upgrade or `apply-config --staged` that adds a volume creates
it while the running image runs, enrolled with the fallback secret chalkctl sends, and
`storage.json` gains it. Every boot sets up the volumes `storage.json` records, so the running
image mounts the new volume as well, also after a fallback, although its config does not name it.
Nothing at boot removes a volume. A slot whose config lacks the volume counts as removing it only
when that slot is written again, so the next config for that image must name the volume too.

That makes the order of two deliveries matter while an image is staged. A config for the running
image that adds a volume the staged image's config lacks would leave the staged image a slot that
counts as removing the volume, and the next write of that slot would be refused. So the node
refuses the config and names the first step: deliver the config to the staged image with
`chalkctl apply-config --staged <node>`, which creates the volume, then to the running image.

The check runs one way. A write of the running image's slot that grows the machine checks the
staged image's slot against the storage it makes. A write of the staged image's slot, by an
upgrade or `apply-config --staged`, does not check the running image's slot: the running image
mounts the new volume, and the next `apply-config` to it must name the volume.

A control plane's addresses are not refused, but they are pinned once it becomes an
[etcd member](../reference/glossary.md#etcd-member): every later boot waits for exactly those
addresses, so a `Network` change that drops one keeps the control plane from starting etcd.

## What does a node do with its config?

`chalkos-config.service` applies the running image's slot at every boot, before the network
starts, and chalkd applies a new set the same way after every `apply-config` to the running image:

- it writes the networkd units to `/run/systemd/network`, where they take precedence over the
  image's own;
- it writes the time servers for chrony and sets the hostname;
- it writes the Kubernetes configuration chalkd and the kubelet read;
- it writes each document to `/run/chalkos/config/<group>/<Kind>/<name>.json`, except `Secret`
  and `Addon` documents;
- it writes, for each service that reads config, the values that the `secretRef` fields of its
  kinds name into the service's credentials.

The storage section is machine state: only chalkd writes `storage.json`, when a config that
changes the storage is delivered, and every boot sets up the volumes it records.

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
chalkd restarts the service, as its
[`restart.when`](../reference/options.md#chalkosconsumersrestartwhen) rule says by default;
`restart.when = "false"` keeps the service running. It restarts a service that runs or stays
active after it ran, and starts a failed one again, since the new config may be what it lacked; a
service that never started or was stopped stays so. `apply-config` prints the units it restarted.
chalkos's own renderings have consumers too: networkd reloads when a `Network`, `NetDev` or `Link`
document changes, or a `Secret` one of them references, chrony reloads its sources when the
`TimeSync`'s servers change, and chalkd restarts the kubelet when the node's Kubernetes settings
change.

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
  With an image staged, status compares the staged slot's config the same way, and one that
  differs shows `not the inventory's; chalkctl apply-config --staged <node> delivers it`.
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
- A kind has one version per image, and there is no conversion between versions. A new version of
  a kind is a new set of documents.
- A rule compares a config with the running image's config alone, and chalkctl runs only the
  rules that read no facts; see [Change rules and facts](rules.md#limits).

## Related pages

- [Variants and platforms](variants.md) for the images that accept the config and how an extension
  declares a kind.
- [Architecture](architecture.md) for where the inventory and the image directories meet chalkctl.
- [Change rules and facts](rules.md) for how an image decides which changes a node accepts.
- [Storage and encryption](storage.md) and [Networking](networking.md) for what the storage and
  network kinds do on the node.
- [Config kinds](../reference/kinds.md) for every field of every kind, the inventory's included.
- [`chalkctl validate`](../reference/cli/chalkctl_validate.md) and
  [`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) for the commands.
- The [quick start](../getting-started/index.md) and
  [Install on bare metal](../guides/install-bare-metal.md) for config files in use.
