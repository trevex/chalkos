---
title: "Testing"
description: "Unit, namespace, evaluation and end-to-end tests, and when to write which"
---

# Testing

chalkos has four kinds of tests: Go unit tests, tests in a network namespace or on disk image
files, Nix evaluation and build checks, and end-to-end tests that boot virtual machines. Each
change is proved at the cheapest level that can prove it, because the VM tests take minutes and
gigabytes of memory each, and the whole suite runs on a developer's machine. This page explains
each kind, where it lives and how to run it.

## Which test to write?

Start at the top of the table and go down only when the level above cannot show the behaviour.

| Kind | Where | Proves | Cost |
| --- | --- | --- | --- |
| Go unit test | next to the code, `*_test.go` in `pkg/` and `cmd/` | logic, parsing, error paths, command output | seconds |
| Namespace or disk-image test | `pkg/kubernetes/vip`, `pkg/install/parts_test.go`, `pkg/upgrade/lab_test.go`, the `vxlan-rule` and `router-firewall` checks | network rules, partition tables and file systems written by the real tools | seconds |
| Evaluation test | `test/nix/eval.nix`, `test/nix/kinds.nix` | the arguments of `mkImages`, option defaults, assertions, what a module sets in an image, config kinds | one evaluation |
| Build check | `nix/checks/` | what an image build produces: kernel modules, sizes, the bootspec | one image build |
| End-to-end test | `test/e2e/` | behaviour that needs a booted node: firmware, Secure Boot, the TPM, the initrd, a cluster | minutes and 2 GiB or more per VM |

## Go unit tests

Unit tests run with `go test` and take no privileges. Commands are tested in-process through
`NewCommand`, with their output captured. Run them in the [development shell](development.md):

```sh
go test ./pkg/... ./cmd/...
```

The `go-unit` check runs the same with `-v` in the Nix sandbox and fails when any test was
skipped or none ran, because a test skips when a tool or variable it needs is missing; a check
that passed on skipped tests would prove nothing.

The `go-lint` check fails when gofmt would change a Go file, or when go vet or staticcheck
reports a finding in `pkg/`, `cmd/` or `test/`. It compiles the end-to-end tests without running
them, so it also catches their dead code. [Development setup](development.md#format-and-lint)
shows how to run the same tools in the development shell.

## Tests in a namespace or on a disk image

Some behaviour depends on the kernel or on real tools, but not on a booted node. These tests
run them in isolation:

- The VIP tests in `pkg/kubernetes/vip` re-run themselves in
  a new user and network namespace, where they add addresses to interfaces and capture the
  frames that announce them.
- The install tests in `pkg/install/parts_test.go` and the upgrade tests in
  `pkg/upgrade/lab_test.go` partition a disk image file with the real sfdisk, and
  systemd-repart for an install, and stand in for the ESP's
  mount, LUKS, the TPM and the firmware's variables with directories and fakes. They stop an
  install or upgrade before each change it makes and check that the disk still boots, so they
  cover interruptions that a VM test could reach only one at a time.
- The `vxlan-rule` check runs `chalkd kubernetes vxlan-rule` as a worker's unit does, with the
  worker's firewall as NixOS renders it, in a network namespace, with a peer and a pod in
  namespaces of their own, and sends UDP to check what the VXLAN rule accepts. The unit tests of
  `pkg/kubernetes/vxlan` cover what the rule makes of its files.
- The `router-firewall` check runs a router image's `chalkos-firewall.service` with the rulesets
  that chalkos-render's golden test keeps, in a network namespace with a LAN peer and a WAN peer
  in namespaces of their own. It connects, pings and spoofs between them to check what the
  ruleset accepts, forwards and translates, and that a reload replaces the table alone.

Prefer this level for anything about partitions, firewall rules or interfaces.

## Evaluation tests and build checks

`test/nix/eval.nix` holds evaluation tests in the form of `lib.runTests`: each `test*`
attribute has an `expr` and an `expected` value. They evaluate images with `mkImages` and check
its arguments, the images' options and assertions, and what the images set; a test that an
evaluation fails uses `builtins.tryEval`. The `eval` check fails with the list of failures, and
`eval-kinds` does the same for `test/nix/kinds.nix`, the tests of config kinds. Both take
`fails` and `worker` from `test/nix/lib.nix`.

```nix
testVariantSetsDiskOptions = {
  expr = (worker [ { chalkos.disk.espSize = "256M"; } ]).chalkos.disk.espSize;
  expected = "256M";
};
```

Build checks test what an image build produces, without booting it. They live in `nix/checks/`:
`image.nix` checks an image's files, `units.nix` runs an image's unit scripts in namespaces, and
`repo.nix` validates the lab template and the examples. `e2e.nix` holds the end-to-end checks,
`eval.nix` the evaluation tests, and `lib.nix` what they share. The test scripts of `units.nix`
are `writeShellApplication` scripts, so shellcheck runs on them when they build.

| Check | What it checks |
| --- | --- |
| `kernel-modules` | An image's module tree holds its groups' modules and no others, and the tool refuses unknown modules and missing dependencies. |
| `image-size` | The test images stay within the ceilings in `nix/testing/image-sizes.nix`, and the fit check every image build runs refuses images that leave no room. Its measure of each image agrees with `pkg/verity`. |
| `bootspec` | An image's bootspec names the UKI on the ESP and no initrd. |
| `vxlan-rule` | The VXLAN firewall rule, in network namespaces. |
| `router-firewall` | A router's firewall unit and rulesets, in network namespaces. |
| `frr-credentials` | A router's `frr.service` fills its config's passwords in from its credentials, and refuses those FRR would not take. |
| `rules` | The images' rules compile, their outputs render their examples and pass their checks, and a broken rule, effect, output or diagnostic fails its image. |
| `template-lab` | The lab template's image builds, and `chalkctl validate` accepts the template's YAML with it. |
| `example-homelab` | The homelab example's image builds, and `chalkctl validate` accepts its YAML, `Rack` documents included, with it. |

## End-to-end tests

The end-to-end tests in `test/e2e` boot chalkos images in QEMU with OVMF, Secure Boot and a software
TPM, through the same `pkg/lab` code chalklab uses, and drive
the nodes with the real chalkctl. Each check runs one test or a group of them:

| Check | Test | VMs |
| --- | --- | --- |
| `e2e-firmware` | The firmware with a TPM starts and reaches the serial console. | 1 |
| `e2e-image` | The test image boots without Secure Boot. | 1 |
| `e2e-secureboot` | A signed image boots under Secure Boot and an unsigned one is refused. | 1 per test |
| `e2e-verity` | A tampered store fails to read. | 1 |
| `e2e-install` | An install in place, with a second disk. | 1 |
| `e2e-installer` | The installer installs a node onto a blank disk under Secure Boot: an install streamed from a directory and cancelled while it writes slot A, continued by one the installer pulls from a registry the test serves, with credentials. | 1 |
| `e2e-iso` | The installer's ISO, signed as chalkbuild signs it, boots. | 1 |
| `e2e-storage` | Volumes on the system disk and a data disk. | 1 |
| `e2e-upgrade` | Under Secure Boot, an upgrade that the node pulls through a mirror endpoint, a registry the test serves, with the endpoint's credentials, that boots from slot B and is found healthy, then one streamed from a directory that never becomes healthy and falls back. | 1 |
| `e2e-kubernetes` | A dual-stack cluster of a control plane and a worker through chalklab: networking, DNS, a private image pulled through a mirror with the node's credentials, renewals and rotations, a reboot. | 2 |
| `e2e-kubernetes-ha` | Three control planes of an IPv6-only cluster behind a VIP: joins, failover, reinstalls and a rolling upgrade. | 3 |
| `e2e-router` | Two routers through chalklab, peering unnumbered over BGP with BFD and a password: the session in their status, a default route and the NAT64 prefix learned, a diagnostic, a delivery FRR reloads without ending the session, the Kubernetes API unimplemented, DNS64's answer for `ipv4only.arpa`, and a connection through NAT64 and QEMU's user-mode network to a listener of the test, with its session in the status and in `nat64-sessions`. | 2 |

A VM gets 2 CPUs and 2 GiB of memory unless its test asks for less, on a 16 GiB sparse disk.
The Kubernetes test gives its control plane 2048 MiB and its worker 1024 MiB; the HA test gives
each control plane 1536 MiB, 4.5 GiB for the three. The router test gives each router 768 MiB.

The Kubernetes checks pull their container images from a registry the test serves from the Nix
store. The `e2e-kubernetes-online` app runs the Kubernetes test with images pulled from the
upstream registries instead, so it needs network access and runs on the host rather than as a
check:

```sh
nix run .#e2e-kubernetes-online
```

### Keep the VM tests few and small

Every VM test adds minutes to the suite and memory to the machine that runs it, and the whole
suite runs on a developer's machine, not only in CI. Hold to these rules:

- Prove behaviour with unit, namespace, disk-image and evaluation tests first.
- Add VM coverage only for what needs a booted node: firmware, Secure Boot, the TPM, the
  initrd, systemd units, or several nodes together.
- Add assertions to an existing end-to-end test rather than a new check. The Kubernetes and HA
  tests already bring a cluster up; a new cluster feature is checked there.
- Keep a VM's memory at what the test needs, and state the memory, CPUs and running time of a
  new or changed VM test in its change.

## Flake checks

Every check builds with `nix build`, and a change runs the checks of the parts it touches:

| Part changed | Checks |
| --- | --- |
| Go code in `pkg/` or `cmd/` | `go-unit`, `go-lint`, and the end-to-end checks covering the behaviour |
| End-to-end tests in `test/e2e` | `go-lint`, and the end-to-end checks of the changed tests |
| `api/chalkos/*/v1/*.proto` | `api-generated`, `docs-generated`, `go-unit`, `go-lint` |
| Command help, option descriptions | `docs-generated`, `docs` |
| Pages in `docs/` | `docs` |
| Modules in `modules/` or `lib/` | `eval`, `eval-kinds`, and the build and end-to-end checks of what the module does |
| `examples/homelab` | `example-homelab` |
| Kernel modules, closure, image size | `kernel-modules`, `image-size`, `bootspec` |
| The firewall or VXLAN rule | `vxlan-rule`, `go-unit`, `e2e-kubernetes` |
| The router's kinds, outputs or firewall | `router-firewall`, `rules`, `eval`, `eval-kinds`, `go-unit` |
| The router's BGP: its kinds, `frr` output, FRR's unit, `pkg/router` | `rules`, `frr-credentials`, `eval`, `eval-kinds`, `go-unit`, `e2e-router` |
| The router's NAT64 and resolver: their kinds, the `jool` and `unbound` outputs, their units, `pkg/router` | `rules`, `eval`, `eval-kinds`, `go-unit`, `image-size`, `e2e-router` |
| `templates/lab` | `template-lab` |
| `.github/workflows` | `workflows` |

Run one check, with its log:

```sh
nix build -L .#checks.x86_64-linux.e2e-upgrade
```

Run the whole suite, which needs `/dev/kvm` and a Nix builder with the `kvm` system feature for
the end-to-end checks:

```sh
nix flake check
```

## When an end-to-end test fails

A failed test keeps its VM directory, with the console log, the firmware variables, the TPM's
state and the disk overlay, and logs the directory's path and the last 200 lines of the
console. In the Nix sandbox the directory is gone once the build ends, so the console lines in
the build log are what is left; `nix log` prints that log again afterwards. To keep the
directory, run the test binary on the host the way the `e2e-kubernetes-online` app does:
`nix build .#chalklab-e2e` builds it, and it reads the same variables the check sets.
