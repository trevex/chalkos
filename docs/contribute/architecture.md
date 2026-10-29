---
title: "Architecture for developers"
description: "Where the code lives: the Go packages, the Nix modules and how they talk to each other"
---

# Architecture for developers

chalkos is two halves: Nix modules that build a cluster's images, and Go programs that read the
cluster's YAML and install and operate the nodes. This page maps both to the repository and shows
where they meet. [Architecture](../how-it-works/node/architecture.md) explains the system as a user sees it;
this page is for changing it.

## Nix and Go meet at the image and the node's config

Go code never evaluates Nix itself. Nix builds images; everything about a cluster or a node is
YAML that Go reads. They meet in these places:

- The image directory. `chalkos.lib.mkImages` builds one per
  variant and platform
  from the node modules (`lib/images.nix`, `modules/node`): the raw image with its partitions,
  `metadata.json` (variant, platform, root hash, revision and tag), `image.json` (what the image
  fixes about Kubernetes) and `schemas/`, the JSON Schemas of the
  config kinds the image accepts. chalkctl and chalklab read
  them from there, or from a running node through the Contract method.
- Files the image carries in `/etc/chalkos`, written by the node modules: the same
  `kubernetes/image.json` and `schemas`, the units granted each kind in `services.json`, the
  outputs a node renders in `outputs.json`, what a delivery restarts or reloads in
  `effects.json`, the system region's repart definitions in `repart.d`
  and the OS CA certificate in `os-ca.crt` when `mkImages` has
  an `osCA`; and the image's own description in `/etc/os-release`. chalkd reads them on the node.
- A node's config: the YAML
  config documents an inventory gives the node,
  which chalkctl reads (`pkg/inventory`), checks against the schemas of the image the inventory
  names for the node and translates into the identity the
  node applies (`pkg/config`). chalkctl delivers the documents to
  chalkd, which checks them again against its own image's
  schemas, keeps them on STATE and translates them the same
  way.
- The node's Kubernetes cluster, which chalkd writes from the `Cluster` and `Flannel`
  documents and the image's `image.json` into `/run/chalkos/kubernetes/cluster.json`. chalkd renders
  chalkos's add-ons (RBAC, kube-proxy, flannel, CoreDNS) and the kubelet's configuration from it
  in Go (`pkg/kubernetes/addons`, `pkg/kubernetes/node`) and applies the objects of the
  `Addon` documents after the add-ons.

The diagram follows an image and a node's YAML to a running node.

```mermaid
flowchart LR
    modules["node modules<br/>modules/node"] -->|mkImages, nix build| image["image directory<br/>schemas/, image.json, metadata.json"]
    yaml["inventory and YAML documents"] -->|reads| chalkctl["chalkctl<br/>pkg/inventory, pkg/config"]
    image -->|schemas, image.json| chalkctl
    chalkctl -->|install, config over the node API| chalkd["chalkd<br/>pkg/chalkd, pkg/chalkd/layers"]
    image -->|/etc/chalkos, os-release| chalkd
    chalkd -->|config| state["STATE"]
```

## Programs in cmd

Each directory of `cmd/` is a main that wires a package up; the logic lives in `pkg/`.

| Program | What its main does |
| --- | --- |
| `cmd/chalkctl` | Runs `chalkctl.NewCommand()` with a context that Ctrl+C cancels. |
| `cmd/chalklab` | The same for `chalklab.NewCommand()`. |
| `cmd/chalkbuild` | The same for `chalkbuild.NewCommand()`. |
| `cmd/chalkcfg` | Runs `chalkcfg.NewCommand()` on a node. |
| `cmd/chalkd` | Dispatches the commands the node's units run: `serve` (chalkd.service), `load-config` (chalkos-config.service), `health` (chalkos-health.service) and each layer's commands as `chalkd <layer> <command>`, such as `chalkd kubernetes prepare` (chalkos-kubernetes.service). Lists the layer plugins compiled into chalkd in `plugins.go`, and loads the credentials `serve` starts with. |
| `cmd/chalkos-networkd-schema` | Writes the spec schemas of the networkd kinds from systemd's gperf tables, for the image build. |
| `cmd/chalkos-render` | Renders the outputs of the `lib.chalkos.render.template` and `.documents` renderers and the built-in outputs `networkd`, `router-sysctl`, `router-nftables`, `frr`, `jool` and `unbound` on a node, in an output's sandbox, and a router image's fallback ruleset and FRR config when the image is built. |
| `cmd/chalkos-rules` | Checks an image when it is built: every rule compiles within the cost limit and reads facts the image declares, the effects act on units and outputs the image has, and every output renders its examples and passes its check. |
| `cmd/chalkos-storage` | Opens STATE and sets up the volumes in the initrd, runs as a systemd generator for the other volumes, and prints the storage facts the storage kinds' rules read. |
| `cmd/docgen` | Writes the command reference and checks the commands the documentation shows. |

## Packages in pkg

| Package | What it does |
| --- | --- |
| `api/kubernetes/v1`, `api/node/v1`, `api/router/v1` | The messages and Connect clients and servers of the node API and of the Kubernetes and router layers' APIs, generated by `buf generate`. |
| `atomicfile` | Replaces a file in one step through a synced temporary file, and syncs directories, so a crash leaves the old content or the new. Writes a new file that must not exist yet. |
| `chalkbuild` | The chalkbuild command tree. |
| `chalkcfg` | The chalkcfg command tree: reads the documents below `/run/chalkos/config` and the kinds the image's schemas name, and prints them. |
| `chalkctl` | The chalkctl command tree, and the list of its layer plugins in `layers.go`. |
| `chalkctl/layer` | The interfaces of chalkctl's layer plugins: the plugin, its optional hooks and the `Host` through which a plugin reaches the cluster and its nodes. |
| `chalkctl/layers/kubernetes` | chalkctl's Kubernetes layer: the commands that run Kubernetes and etcd and revoke nodes, the Kubernetes section of a node's status, the facts of its images, the share it delivers, an upgrade's quorum checks, drains and uncordons, and the rotations of the Kubernetes secrets. |
| `chalkctl/layers/router` | chalkctl's router layer: `chalkctl node routes` and the router's section of a node's status. |
| `chalkd` | The node API server, with the permissions of each NodeService method; the registry of layer plugins it mounts, starts and asks for hooks; and chalkd's work at boot: the node certificate's renewal and the health check. |
| `chalkd/layer` | The interfaces of chalkd's layer plugins, their optional hooks and the registry of the compiled and active plugins. |
| `chalkd/layers/kubernetes` | chalkd's Kubernetes layer: KubernetesService, the node's Kubernetes loops, the control plane's, the join and the VIP election, the issuer of node certificates, and the `chalkd kubernetes` commands and actions. |
| `chalkd/layers/router` | chalkd's router layer: RouterService, with the state of the node's FRR, Jool and Unbound and the kernel's routes. |
| `chalkd/renewal` | Renews a certificate, or a set of them, once it is due, for the node certificate and a layer's own. |
| `chalklab` | The chalklab command tree. |
| `client` | Connections to chalkd, each with one way to verify the node: by fingerprint in maintenance mode, by the OS CA otherwise; the names chalkd's status gives certificates and trusted values. |
| `clustersecrets` | The secrets file: the OS CA and node CA with their keys, the Kubernetes secrets, the recovery secret, the file's age encryption, the rotation of what it holds, and the lock and in-place replacement of the file, which keeps its previous version. |
| `command` | Runs a program of the image's store with bounded output, for command facts and outputs; a failure keeps standard error out of its error. |
| `config` | Config documents: parsing, the rules of a set, defaults, the hash, and the translation into the identity the node applies. |
| `config/slot` | Keeps a node's config on STATE, a slot per image with its identity, and writes it for the node's units: the documents below `/run/chalkos/config` and the secrets the granted units load as credentials. |
| `config/withheld` | Names the kinds the node withholds from its services, Secret and Addon, without importing anything, so chalkcfg shares the list with chalkos-rules and `config`. |
| `diagnostics` | Reads the image's `diagnostics.json`, checks a call's arguments against their parameters' types, builds the command line, and runs it as a transient systemd unit with a sandbox of its own. |
| `effects` | Reads the image's `services.json`, the units granted config kinds, and `effects.json`, what a delivery does to those units and to chalkd, and refuses an effect chalkd could not carry out. |
| `esp` | Lists the UKIs on an ESP with the image and generation each boots, and stages, removes and names a UKI, for an install and an upgrade. |
| `facts` | Loads the facts change rules read: files, and commands of the image's store run in a sandbox. |
| `identity` | The node's identity, the form its config translates to, which the node applies at boot and its Kubernetes side reads. |
| `image` | Reads a disk image: its partitions from `repart-output.json`, its store and hash tree, the UKI and boot loader off its ESP, and the provenance `metadata.json` names. |
| `imagesign` | Signs an image's boot loader and UKIs, in place or in a signed copy of an image directory. |
| `install` | Installs a node in place or from the installer, step by step and repeatable: STATE, the ESP, the UEFI boot entries, wiping the target and the system region's definitions each have a file, and `parts.go` holds the steps of an install from the installer. |
| `internal/cli` | What the command trees of chalkctl, chalklab, chalkbuild and chalkcfg share: usage errors and their exit status, the root and group commands and positional argument checks. |
| `internal/filecopy` | Copies a disk image without its holes and a plain file to a new one, for chalklab, the lab and image signing. |
| `inventory` | Reads chalkctl's inventory: the cluster's nodes, the address chalkctl reaches each at, the image each runs and the config files each gets. |
| `jsonfile` | Reads a JSON file an image may leave out, such as `facts.json`, `effects.json` or `outputs.json`. |
| `kubernetes` | The node's Kubernetes cluster as `config` builds it from the config and the image's facts, its flags and address ranges, the node as its identity names it, the kinds of Kubernetes nodes (control plane and worker), and the control plane's ports and paths on a node. |
| `kubernetes/addons` | Renders the objects chalkos applies to every cluster before its own manifests: RBAC for chalkos's identities, kube-proxy, flannel and CoreDNS. |
| `kubernetes/apply` | Server-side apply of manifests and approval of kubelet serving certificates. |
| `kubernetes/credprovider` | The kubelet's image credential provider: writes its config and the node's registry credentials from the Registry documents, and answers the kubelet's requests. |
| `kubernetes/etcd` | etcd membership: learners, promotion, quorum-safe removal, the node's own member; finds the members from etcd's mirror pods and counts encrypted values by key. |
| `kubernetes/manifests` | The control plane's static pods. |
| `kubernetes/node` | The node's Kubernetes files at boot: addresses, kubelet credentials and flags, control-plane certificates, the check that they are prepared, the removal of the static pods. |
| `kubernetes/nodeip` | Picks the node's address of each family. |
| `kubernetes/pki` | The Kubernetes certificates: the share, the control plane's leaves, admin kubeconfigs. |
| `kubernetes/revoked` | The ConfigMap of revoked nodes, which control planes read before they renew a node certificate, and the admission policy that shuts those nodes out. |
| `kubernetes/vip` | The etcd election of the control plane that holds the VIPs; adds a VIP to an interface and announces it. |
| `kubernetes/vxlan` | The nftables table `chalkos-vxlan`: marks VXLAN to the node's addresses for the firewall and drops pods' VXLAN to the source ranges. |
| `lab` | QEMU VMs, swtpm, the lab switch and console and the lab's supervisor, for chalklab and the end-to-end tests. |
| `layers` | Reads the layers an image carries from their markers in `/etc/chalkos/layers`, for the plugins chalkd activates and the `layers` of Info and Contract. |
| `networkdschema` | Generates the spec schemas of the networkd kinds from systemd's gperf tables. |
| `nodeconfig` | Applies the running image's config slot, chosen by `usrhash=`, at boot (`chalkd load-config`) and after every apply: the hostname and `/run/chalkos/node.json`; writes the config's documents and secrets for the granted units, and renders the outputs at boot, networkd's units and chrony's servers among them. |
| `ociimage` | Reads, writes, pulls and pushes chalkos images as OCI artifacts: one manifest whose config is the image header and whose layers are the store, hash tree, UKI, boot loader and contract. |
| `outputs` | Renders and checks the image's outputs in a sandbox of their own, as an unprivileged user, swaps them in by a symlink rename, and records their hashes and failures. |
| `pki` | The OS CA and node CA, client and node certificates and their checks, client roles, PEM keys and certificate bundles. |
| `router` | Reads a router's state: its BGP sessions and paths from vtysh's JSON, its NAT64 instances, counters and pools from jool's CSV, the kernel's routes from netlink, and the filter of a `Routes` call. |
| `rules` | Compiles the change rules of an image's schemas, with chalkos's CEL functions, and evaluates them against a config, the config it replaces and the node's facts. |
| `schema` | Reads an image's config contract: `metadata.json` and the JSON Schemas of its kinds, and validates documents against them. |
| `secureboot` | Checks an EFI binary's Authenticode signature as Secure Boot firmware does, against db and dbx read from the firmware or against given certificates. |
| `storage` | Storage sections, disk references and pins, repart definitions, and the changes a delivered section makes to the recorded one. |
| `storage/gpt` | Reads and changes partition tables with sfdisk under the disk's lock, waiting a bounded time for it, and names the partition types of the system region. |
| `storage/node` | Storage on the node: STATE, volumes, VAR and the generator's units; the storage changes chalkd makes and the volumes an install creates and enrols; the blkid probe. |
| `storeslot` | The store slots of a disk and the writer an upgrade and an install share: retire a slot, write and verify the store, activate the slot. |
| `tool` | Runs the command-line tools chalkd, the installer and the storage code drive, such as cryptsetup, sfdisk and systemctl. |
| `uki` | Reads a UKI's os-release and command line and an EFI binary's architecture, and refuses an image version systemd-boot would order its entry by. |
| `upgrade` | Writes an image into the inactive slot and makes it the next boot; the image header and the checks of its UKI and boot loader an install shares; the booted entry and the provenance record. |
| `verity` | Reads and checks dm-verity hash trees. |

Test helpers live in packages of their own:

- `uki/ukitest`: small UKIs.
- `image/imagetest`: small disk images, db signers and `sbsign`.
- `chalkd/layer/layertest`: checks of a layer's API against its plugin's permissions.
- `chalkd/layers/kubernetes/kubernetestest`: a node's Kubernetes files, shares and CAs, and
  stand-ins for the preparation, the API server's Node and chalkd.
- `chalkd/renewal/renewaltest`: a fake clock that drives a renewal.
- `kubernetes/etcd/etcdtest`: in-process etcd members.
- `ociimage/ocitest`: in-memory OCI registries and small chalkos images.
- `pki/pkitest`: node CAs.
- `storage/storagetest`: the sysfs, udev and `/dev` trees of a host's disks.
- `tool/tooltest`: a fake tool runner that records commands and answers them by prefix.

## The node API

The API is defined in three files below `api/chalkos/`: `node/v1/node.proto` holds NodeService,
which every image serves, and `kubernetes/v1/kubernetes.proto` and `router/v1/router.proto` hold
the APIs of the Kubernetes and router layers. chalkd serves NodeService over Connect on TCP port
50000 with mutual TLS, and mounts each compiled layer plugin's service beside it. Which client
role may call a method, and in which mode, is one table per API: `permissions` in
`pkg/chalkd/server.go` for NodeService, and each plugin's `Permissions`, such as that of
`pkg/chalkd/layers/kubernetes`.

```go
nodev1connect.NodeServiceInfoProcedure: {Modes: layer.AnyMode, Role: pki.RoleReader},
kubernetesv1connect.KubernetesServiceBootstrapProcedure: {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
```

A new method needs an entry there; the authoriser refuses a method without one. It refuses a
method of a layer the image lacks with `UNIMPLEMENTED` before it checks the mode. A handler of a
layer's method therefore runs only on a node in normal mode whose image carries that layer, and
does not check for a missing layer itself. One check sits outside the tables: `Upgrade` needs
the admin role when its header carries config or a fallback secret, as `ApplyConfig` does. The
table cannot see the header, so `Upgrade` checks that itself. Comments in the proto files become
the [Node API](../reference/api.md) page, so they are written for users.

## Layer plugins

A layer, such as Kubernetes or the router, is a plugin compiled into chalkd and chalkctl. The
image says which layers it carries with its markers in `/etc/chalkos/layers`, and a node's Info
names them.

`pkg/chalkd/layer` declares chalkd's side. A `Plugin` has a name, its API's handler, the
permissions of its methods and the background loops it starts in normal mode. Optional hooks,
which chalkd finds by type assertion, add what else the layer does:

| Hook | What the layer adds |
| --- | --- |
| `Health` | Its check of a new boot's health. |
| `Certificates` | Its certificates and trusted CAs in Status. |
| `Delivery` | The payloads it takes with a delivery, its part of the identity, and what it writes from the config. |
| `Issuer` | The issuing of node certificates on renewal; an image has one issuer at most. |
| `ImageFacts` | The facts of its images that config is translated with. |
| `Commands` | Subcommands, run as `chalkd <layer> <command>`. |
| `Actions` | Actions on what the running chalkd holds, which effects name as `<layer>:<action>`. |

`cmd/chalkd/plugins.go` lists the compiled plugins, and `layer.Registry` sorts them into the
compiled and the active ones. chalkd mounts the API of every compiled plugin, so the authoriser
can answer for a missing layer, and runs the commands of every compiled plugin. It starts only
the active plugins, whose layers the image carries, and asks them for most hooks. For what may
belong to another image, it asks every compiled plugin: the facts of the image an upgrade or an
install writes, the identity an upgrade's slot keeps, and the payloads the installer takes. The
comment of each hook in `pkg/chalkd/layer/layer.go` names the plugins chalkd asks. The plugins
live in `pkg/chalkd/layers/kubernetes` and `pkg/chalkd/layers/router`.

`pkg/chalkctl/layer` declares chalkctl's side in the same way: a `Plugin` with hooks for commands,
a node's status section, image facts, the payloads of a delivery and their admission, an
upgrade's checks and a rotation's steps. chalkctl asks every compiled plugin for what the
inventory and the secrets file drive, such as commands, payloads and rotations. For what it reads
from a node's layer services, such as the node's status section and its part in an upgrade, it
asks only the plugins of the layers the node's Info names. A plugin reaches the cluster and its
nodes through the `Host` chalkctl gives it. `pkg/chalkctl/layers.go` lists the plugins, which live in
`pkg/chalkctl/layers/kubernetes` and `pkg/chalkctl/layers/router`.

## chalkd runs with a narrow sandbox

The unit `chalkd.service` is declared in `modules/node/chalkd.nix`. chalkd runs as root with its
capabilities, because it partitions disks, enrols TPM keyslots, writes EFI variables, mounts
volumes the host must see and sets the hostname. Its unit therefore has no file system or
hostname namespace. `ProtectKernelLogs` stays off too, because systemd would give chalkd a mount
namespace for it, and chalkd's mounts would not reach the host.

The sandbox it does have:

- `RestrictAddressFamilies` allows only the socket families chalkd uses: Unix, IPv4, IPv6,
  netlink, the kernel crypto API (`AF_ALG`, for cryptsetup and systemd-cryptenroll) and packet
  sockets (`AF_PACKET`, for the gratuitous ARP of a VIP).
- `NoNewPrivileges` forbids new privileges.
- `SystemCallArchitectures=native` fixes the system call architecture.
- `ProtectClock` protects the clock, and `LockPersonality` and `RestrictRealtime` are on.

`Restart=always` with no start limit restarts chalkd whenever it stops, because it is the only
way to reach the node: nodes have no SSH and no login.

## An install runs in fixed steps

`pkg/install` turns a node in maintenance mode into an installed node, and `pkg/chalkd/install.go`
serves it as the node API's Install method. Every step finds what an earlier, interrupted attempt
did and continues from there, so running the same `chalkctl install` again continues an install.

[`chalkctl install`](../reference/cli/chalkctl_install.md) connects to the node, compares its
certificate with the fingerprint from the console, and sends what makes the machine this node.
[Security model](../how-it-works/node/security.md) explains how the fingerprint and `--insecure`
protect the secrets on the way.

On a node that runs its image already, the install happens in place (`Installer.InPlace`), and
chalkctl sends no image. chalkd recreates STATE if its encryption differs from the node's
`SystemDisk`, opens it, creates VAR and the volumes, and enrols the second keyslot of every
encrypted volume unless the fallback is `none`. It then writes the config and certificates, gives
the ESP a random partition UUID and writes the `installed` marker.

On a node that runs the installer (`Installer.FromParts`), chalkctl also sends the image the
inventory names for the node, or the one `--image` gives, in the same parts an upgrade sends,
plus the boot loader. The sequence shows what the installer does with them. When the inventory
names the image in a registry, the header carries the image's reference by digest in place of the
schemas, `image.json` and repart definitions, and no parts follow it. The installer then reads
those from the artifact's contract layer before it touches the disk. It pulls the store, hash
tree, UKI and boot loader from the registry in the steps where the sequence receives them from
chalkctl.

```mermaid
sequenceDiagram
    participant chalkctl
    participant chalkd as chalkd on the installer
    participant disk as Target disk and TPM
    participant fw as Firmware
    chalkd-->>chalkd: console shows fingerprint and addresses
    chalkctl->>chalkd: Install header: config, the image's schemas and image.json, node certificate, OS CA, fallback secret, Kubernetes share, image header, repart definitions
    chalkd->>disk: check the disk is empty or an unfinished install
    chalkd->>disk: lay out ESP, slot A, slot B and STATE with systemd-repart, STATE sealed to PCR 7
    chalkctl->>chalkd: store and hash tree
    chalkd->>disk: write slot A, verify it against the root hash, activate it
    chalkctl->>chalkd: UKI and boot loader
    chalkd->>disk: check the UKI, write it to the ESP
    chalkd->>disk: open STATE, create VAR, enrol the fallback key, write the config
    chalkd->>disk: write the boot loader
    chalkd->>fw: add a boot entry and set BootNext
    chalkd->>disk: write the installed marker
    chalkd-->>chalkctl: done, rebooting
```

Before anything is written, the installer checks the config against the image's schemas,
translates it for the image's platform, and checks that the image is built for the machine's
architecture. With Secure Boot enforced, it checks the UKI's and the boot loader's signatures
against the firmware's db and dbx, as the firmware will at the next boot. It checks each when it
receives it: after slot A is written and before the boot loader is.

The installer takes the disk the node's `SystemDisk` names when blkid finds nothing on it but at
most an empty GPT, or when it holds an unfinished install of the node, which it continues. It
takes any other disk only with `--wipe-disk`. Until the boot loader is written the firmware finds
nothing to boot on the target, so an interrupted install never boots a half-installed node.

The image travels as its parts: the compressed store, its hash tree, the UKI and systemd-boot.
For an image with Kubernetes that is about 300 MiB, instead of the multi-gigabyte raw image.

After the reboot, the initrd unseals STATE and VAR with the TPM, `chalkos-config.service` applies
the config, and chalkd finds the `installed` marker and starts in normal mode with the node
certificate from STATE.

## Storage partitions are matched deterministically

`pkg/config/storage.go` renders the systemd-repart definitions of a node's VAR and volumes, and
`pkg/storage/node` runs systemd-repart once per disk to create and grow the partitions. repart
assigns existing partitions to definitions, so three choices keep that matching deterministic:

- Each partition gets its own GPT type UUID, derived from its label. repart assigns existing
  partitions to definitions by type, so a new volume can never take over another volume's
  partition.
- Definition files are ordered: VAR first, then the system disk's volumes in name order. A disk of
  its own holds one volume.
- Partition UUIDs derive from a seed of the node's name and the disk, passed as `--seed`. The same
  definition yields the same partition UUID on every run, and the node opens volumes on other
  disks by that UUID.

A `Volume`'s `repart` field passes further repart.d keys through. The keys the typed fields set
(`Type`, `Label`, `Encrypt`, `Format`, `SizeMinBytes`, `SizeMaxBytes` and `MountPoint`) are refused
there.

## The command-line programs

chalkctl, chalklab, chalkbuild and chalkcfg are cobra command trees in importable packages:
`pkg/chalkctl`, `pkg/chalklab`, `pkg/chalkbuild` and `pkg/chalkcfg` each export `NewCommand()`,
which returns the root command, and `Execute(ctx, root)`, which runs it and returns the exit code.
Another program can add their trees to its own, and tests run commands in-process with their
output captured. The four share `pkg/internal/cli`: its `Execute` prints a command's error and
exits with 2 and the command's usage after a usage error, such as a wrong number of arguments, an
unknown flag or a required flag not set, and with 1 after another error. chalkctl also exits with
130 after an error once Ctrl+C cancelled it. Each package keeps its help texts in `help.go`, and
a test requires a short and a long description and an example on every visible command, because
they become the [command reference](../reference/cli/index.md). chalkcfg runs on nodes, which have no logins, so
it has no `completion` command.

## Nix modules

| Directory | What it holds |
| --- | --- |
| `modules/node/` | The NixOS modules of every image: `packages.nix` (chalkos's programs, as the module argument `chalkosPackages`), `base/` (appliance, closure, disk, image directory, storage), `kinds/` (`chalkos.kinds`, `chalkos.facts`, networkd's kinds and the built-in kinds), `runtime/` (grants, `chalkos.outputs`, `chalkos.effects`, chalkcfg on the system path), kernel, chalkd, time, the CNI, `kubernetes/` (the kubelet, effects, containerd, registries, firewall), upgrade. |
| `modules/platforms/` | `metal` and `kvm`. |
| `modules/installer/` | The installer image and its ISO. |
| `modules/testing/` | The test probe, the test image's partition sizes and the test-only config kind `Greeting` with its output and effect. |
| `lib/` | `mkImages` (`images.nix`), its arguments as module options (`images-arguments.nix`), `chalkos.nix`, the `lib.chalkos` of every image's modules, and the functions the modules share: `kinds/` (the JSON Schema converter, the chalkcfg commands of `get`, the option types both recognise, the API groups of chalkos's own kinds, and the check of the kinds a grant, output or effect names) and `cel.nix` (CEL that effect rules share). |

## Layout rules

The layout follows these rules, so each change has one place to go:

- An extension is a NixOS module in a variant's
  `modules`: its kinds, its units, their outputs and their effects, together.
- The node modules declare the image options; `lib/images-arguments.nix` declares the
  arguments of `mkImages`. Everything about a cluster or a node is a config kind, not an option.
- Go code reads a node's config documents and the image's files alone.
- `nix/` holds packages and tooling: apps, checks, the development shell, the docs build and
  test fixtures. The scripts the node modules run, such as the control plane's ports, are
  packages there; the modules decide where and with which arguments an image runs them.
- `cmd/` holds the mains, which wire packages up; logic worth testing lives in `pkg/`.

[Development setup](development.md) lists the commands of a development loop and
[Testing](testing.md) the tests each part has.
