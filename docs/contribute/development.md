---
title: "Development setup"
description: "The development shell, the repository layout and the commands of a development loop"
---

# Development setup

This page sets up a chalkos checkout for development and lists the commands of a development
loop: building the tools and images, running the Go tests, regenerating code and pages,
formatting and linting, and running a lab from the checkout. Everything
runs through Nix, so the only requirements are an x86-64 Linux machine with Nix and flakes
enabled, and `/dev/kvm` for anything that boots a virtual machine.

## Enter the development shell

Clone the repository and enter its development shell:

```sh
git clone https://github.com/trevex/chalkos
cd chalkos
nix develop
```

The shell, defined in `nix/shell.nix`, provides:

- Go, gopls, staticcheck, buf with protoc-gen-go, protoc-gen-connect-go and protoc-gen-doc,
  openssl, jq, nixfmt and virt-firmware;
- the tools the tests run: QEMU, swtpm, mtools, sbsigntool, dosfstools, cryptsetup,
  util-linux, e2fsprogs, vde2, socat and iproute2;
- the variables the tests read: `CHALKLAB_OVMF_CODE`, `CHALKLAB_OVMF_VARS` and
  `CHALKLAB_OVMF_VARS_ENROLLED` (OVMF firmware, blank and with test
  Secure Boot keys enrolled),
  `CHALKLAB_SB_KEYS` (those keys), `CHALKOS_TEST_EFI`, `CHALKOS_TEST_REPART` and
  `CHALKOS_TEST_IMAGE_DEFINITIONS`.

Tests that need a tool or variable skip without it, so run them in this shell. The flake checks
use the same tools and variables from `nix/testing/env.nix`.

## Repository layout

| Directory | What lives there |
| --- | --- |
| `api/` | The node API's protobuf definition, `api/chalkos/node/v1/node.proto`, and the template of its reference page. |
| `cmd/` | Thin mains: `chalkctl`, `chalkd`, `chalklab`, `chalkbuild`, `chalkos-storage` and `docgen`. |
| `pkg/` | The Go packages, including `pkg/api`, the code `buf generate` writes. |
| `modules/node/` | The NixOS modules every image is built from. |
| `modules/platforms/` | The `metal` and `kvm` platforms. |
| `modules/installer/` | The installer image and its ISO. |
| `modules/testing/` | The test probe and the test image's small partitions. |
| `lib/` | `mkImages` and the module of its arguments. |
| `nix/` | Tooling: packages, apps, checks, the development shell, the docs build, and the test images and their secrets in `nix/testing/`. |
| `templates/lab/` | The lab template the quick start uses. |
| `examples/homelab/` | An example cluster: a flake that builds its image with an extension, an inventory and its YAML, which the `example-homelab` check builds and validates. |
| `test/e2e/` | The end-to-end tests, which boot virtual machines. |
| `test/nix/` | The Nix evaluation tests. |
| `test/veritysize/` | A helper of the `image-size` check that reads an image's store and hash tree sizes with `pkg/verity`. |
| `test/fixtures/` | Static test data: the e2e clusters' YAML, kind schemas and config documents. |
| `docs/` | The documentation site's pages. |

[Architecture for developers](architecture.md) describes the packages and modules one by one.

## Build the tools

The flake's packages build the programs with Nix, as a user gets them:

```sh
nix build .#chalkctl
nix build .#chalklab
nix build .#chalkbuild
nix build .#chalkd
```

`chalklab` is wrapped with QEMU, OVMF, swtpm and the other tools a lab runs, and with
`chalkctl`, so its closure is larger than chalkctl's. `chalkbuild` is wrapped with mtools and
sbsigntool, which it signs images with. During development, run the commands from the working
tree instead:

```sh
go run ./cmd/chalkctl --help
go run ./cmd/chalklab --help
go run ./cmd/chalkbuild --help
```

chalkd only does useful work on a node; the end-to-end tests and
a lab run it there.

## Build images

A cluster's flake builds its images with `chalkos.lib.mkImages`, whose `images.<variant>-<platform>`
it exposes as packages. The repository's own flake exposes its test images, which
`nix/testing/images.nix` builds, as packages:

| Package | What it is |
| --- | --- |
| `test-image` | The `test` variant on `metal`, with small partitions and the test probe; most VM tests boot it. |
| `test-storage-image` | The `storage` variant on `metal`, which the storage test gives volumes on the system disk and on a second disk. |
| `test-installer` | The test images' installer. |
| `test-kubernetes-image` | The `k8s` variant on `kvm`, which the Kubernetes test's control plane and worker boot. |
| `test-kubernetes-ha-image` | The `k8s` variant on `metal`, which the HA test's control planes boot. |
| `test-router-image` | The `router` variant on `kvm`, which the router test's routers boot. |
| `test-upgrade-image`, `test-unhealthy-image`, `test-kubernetes-ha-upgrade-image` | Images the upgrade tests install. |
| `installer` | An installer of a cluster without an OS CA, which accepts any client until it installs a node. |

```sh
nix build .#test-image
```

The result is a directory with the raw image, `repart-output.json`, `repart.d`, `metadata.json`
and `schemas`, as
[Image and partition layout](../reference/image-layout.md#outputs-of-an-image-build) lists.

## Run the Go tests

Run the unit tests in the development shell:

```sh
go test ./pkg/... ./cmd/...
```

None of them needs root. The tests of `pkg/kubernetes/vip` and `pkg/kubernetes/nodeip` run
themselves again in a new user and network namespace, and a test of `pkg/kubernetes/node` in a new
user and mount namespace, so the kernel must allow unprivileged user namespaces. The install and
upgrade tests lay out disk image files with the real sfdisk and systemd-repart and emulate
mounts, LUKS, the TPM and the firmware, so they need no loop
devices.

`go test ./...` also runs the end-to-end tests in `test/e2e`, which boot VMs as soon as the
variables they need are set; the development shell sets those of the firmware test. Run them
through their flake checks instead, as [Testing](testing.md) describes.

## Regenerate code and pages

The node API's Go code and its reference page come from `api/chalkos/node/v1/node.proto`:

```sh
buf generate
```

The reference pages for the commands, the options and the API come from the code:

```sh
nix run .#docgen
```

Both write into the working tree, and the results are committed. The `api-generated` and
`docs-generated` checks fail when they are out of date.

## Format and lint

```sh
nix fmt
gofmt -l -w cmd pkg test
```

`nix fmt` runs nixfmt over the Nix files; `nix fmt -- --ci` checks them without changing
anything.

Lint the Go code, the end-to-end tests included, with go vet and staticcheck:

```sh
go vet ./pkg/... ./cmd/... ./test/...
staticcheck ./pkg/... ./cmd/... ./test/...
```

staticcheck reads `staticcheck.conf` at the root of the repository, which enables all its checks
but a few and gives the reason for each one it leaves out. A finding that is wrong for one line
takes a `//lint:ignore <check> <reason>` comment above that line. The `go-lint` check runs gofmt,
go vet and staticcheck in the Nix sandbox and fails on any finding.

## Run a lab from the checkout

A lab runs a cluster's nodes as QEMU VMs with Secure Boot and a TPM, which is the quickest way
to see a change on a booted cluster. Create one from the template, pointing its `chalkos` input
at the checkout:

```sh
mkdir ~/lab && cd ~/lab
nix flake init -t <checkout>#lab
```

`<checkout>` is the absolute path of the repository. In the lab's `flake.nix`, replace the
input's URL:

```nix
inputs.chalkos.url = "git+file://<checkout>";
```

Then follow the template's README, with chalklab and chalkctl
now built from the checkout:

```sh
git init && git add .
nix develop
chalkctl gen secrets --plaintext
git add . && git commit -m "A chalkos lab"
chalklab create
```

A `git+file` input reads the files git tracks, so add new files in the checkout before the lab
sees them. After changing the checkout, run `nix flake update chalkos` in the lab to lock the new
state, then upgrade the lab's nodes as the template's README shows, or recreate the lab with
`chalklab destroy` and `chalklab create`. The [quick start](../get-started/quick-start.md) walks
through a lab step by step.

## Serve the documentation

```sh
nix run .#docs-serve
```

This serves the site from the working tree and rebuilds it as pages change.
[Writing documentation](writing-docs.md) covers the rest of the documentation's tooling.
