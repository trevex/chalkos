# chalkos

chalkos is an image-based NixOS for Kubernetes nodes. A cluster is declared in a Nix flake; each
role becomes a disk image with an erofs Nix store on dm-verity and a unified kernel image that
Secure Boot can verify. Nodes keep their state on TPM-sealed encrypted partitions, upgrade
between A/B slots and fall back when a new image does not boot healthy. Nodes have no interactive
logins: `chalkd` on each node serves an API over mutual TLS, and `chalkctl` installs, upgrades
and operates the nodes.

Documentation: https://trevex.github.io/chalkos/

## Quick start

`chalklab` runs a cluster of a control plane and a worker as QEMU virtual machines with Secure
Boot and a TPM on an x86-64 Linux machine with Nix, `/dev/kvm` and about 5 GiB of free memory:

```sh
mkdir lab && cd lab
nix flake init -t github:trevex/chalkos#lab
git init && git add .
nix develop
chalkctl gen secrets --plaintext
git add . && git commit -m "A chalkos lab"
chalklab create
```

`chalklab create` builds the images, starts the VMs, installs and bootstraps the cluster, and
prints how to reach it with `kubectl` and `chalkctl`. `chalklab destroy` removes the lab again.

## Development

`nix develop` gives the tools; `nix flake check` runs the tests, including the end-to-end tests
in QEMU, which need `/dev/kvm`. The documentation is built with `nix build .#docs`, served while
it is edited with `nix run .#docs-serve`, and its generated reference pages are written with
`nix run .#docgen`.
