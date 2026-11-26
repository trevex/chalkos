# chalkos

[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/trevex/chalkos/badge)](https://scorecard.dev/viewer/?uri=github.com/trevex/chalkos)

chalkos builds API-driven, immutable Linux distributions from NixOS modules. It follows the
operating model of [Talos Linux](https://www.talos.dev/): nodes boot a read-only image, have no
logins, and are managed through an API over mutual TLS. Where Talos is one distribution, chalkos
is the toolkit that builds such a distribution for a purpose, with Nix and the NixOS module
system. Two layers ship with it. The Kubernetes layer makes a node much like a Talos node, and the
router layer makes a node a router managed the same way, somewhere between Talos and VyOS.

A Nix flake calls `chalkos.lib.mkImages` with a list of images, each a platform and NixOS modules.
Each image is a disk image with an erofs Nix store on dm-verity and a unified kernel image that
Secure Boot verifies. Nodes keep their state on encrypted partitions sealed to the TPM, upgrade
between A/B slots and fall back to the image before when a new one does not boot healthy. chalkd on
each node serves the node API, and chalkctl installs, configures and upgrades the nodes.

chalkos adds two things to that model, both about config:

- The image carries the schemas of its config. A NixOS module declares a config kind, much as a
  CustomResourceDefinition declares one in Kubernetes, with its fields, defaults and CEL rules.
  The build turns the kinds into JSON Schemas inside the image, so chalkctl checks a node's YAML
  against the exact image the node runs before it sends anything, and the node checks it again.
- A node keeps one config slot per image. An upgrade delivers the config for the new image, which
  that image checks with its own schemas, rules and renderers before the node installs it, while
  the image before keeps the config it ran. A kind can change incompatibly between two images, and
  a fallback still boots with config its image understands.

Nodes also run nothing the image did not bring. They carry no shell, every writable file system is
mounted noexec except the directories a layer runs programs from, such as containerd's root, and
with Secure Boot on the kernel loads modules only from the signed image. The
[security model](https://trevex.github.io/chalkos/how-it-works/node/security/) lists what each
attacker position reaches.

The rest of the toolkit serves the same workflow: an extension API to declare kinds, render config
files, react to changes and add diagnostics; tests of what an image renders, refuses and reloads,
run by `nix flake check`; chalklab, which runs a cluster as QEMU virtual machines; and chalkbuild,
which signs images and publishes them to OCI registries.

chalkos is before 1.0; its options, commands and node API still change between releases.

Documentation: https://trevex.github.io/chalkos/

## Quick start

chalklab runs a cluster of a control plane and a worker as QEMU virtual machines, each with
Secure Boot and a TPM. It needs an x86-64 Linux machine with Nix and flakes, access to
`/dev/kvm` and about 5 GiB of free memory:

<!-- x-release-please-start-version -->
```sh
mkdir lab && cd lab && nix flake init -t github:trevex/chalkos/v0.7.2#lab
nix develop -c chalkctl gen secrets --plaintext
nix develop -c chalklab create --secrets=secrets.json
```
<!-- x-release-please-end -->

`chalklab create` pulls the release's image, starts the virtual machines, installs the nodes,
bootstraps the cluster and prints how to reach it with kubectl and chalkctl. `chalklab destroy`
removes the lab. The [quick start](https://trevex.github.io/chalkos/get-started/quick-start-kubernetes/)
walks through it, and
[Upgrade the lab to your own image](https://trevex.github.io/chalkos/build/upgrade-the-lab/) builds
an image of your own and upgrades the lab to it.

## Development

[CONTRIBUTING.md](CONTRIBUTING.md) sets up the development shell, runs the tests and says how to
commit a change.

## Security

Report a vulnerability privately, as [SECURITY.md](SECURITY.md) describes, never in a public issue.

## Licence

chalkos is licensed under the [Apache License 2.0](LICENSE). The images and the installer of a
release also contain third-party software, each component under its own licence; [NOTICE](NOTICE)
says where to get their source.
