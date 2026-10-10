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
| Namespace or disk-image test | `pkg/kubernetes/vip`, `pkg/install/parts_test.go`, `pkg/upgrade/lab_test.go`, the `vxlan-rule` check | network rules, partition tables and file systems written by the real tools | seconds |
| Evaluation test | `test/nix/eval.nix` | option defaults, assertions, what a module sets in a role image, the manifest | one evaluation |
| Build check | `nix/checks.nix` | what an image build produces: kernel modules, sizes, the bootspec | one image build |
| End-to-end test | `test/e2e/` | behaviour that needs a booted node: firmware, [Secure Boot](../reference/glossary.md#secure-boot), the [TPM](../reference/glossary.md#tpm), the initrd, a cluster | minutes and 2 GiB or more per VM |

## Go unit tests

Unit tests run with `go test` and take no privileges. Commands are tested in-process through
`NewCommand`, with their output captured. Run them in the [development shell](development.md):

```sh
go test ./pkg/... ./cmd/...
```

The `go-unit` check runs the same with `-v` in the Nix sandbox and fails when any test was
skipped or none ran, because a test skips when a tool or variable it needs is missing; a check
that passed on skipped tests would prove nothing.

## Tests in a namespace or on a disk image

Some behaviour depends on the kernel or on real tools, but not on a booted node. These tests
run them in isolation:

- The [VIP](../reference/glossary.md#vip) tests in `pkg/kubernetes/vip` re-run themselves in
  a new user and network namespace, where they add addresses to interfaces and capture the
  frames that announce them.
- The install tests in `pkg/install/parts_test.go` and the upgrade tests in
  `pkg/upgrade/lab_test.go` partition a disk image file with the real sfdisk, and
  systemd-repart for an install, and stand in for the [ESP](../reference/glossary.md#esp)'s
  mount, LUKS, the TPM and the firmware's variables with directories and fakes. They stop an
  install or upgrade before each change it makes and check that the disk still boots, so they
  cover interruptions that a VM test could reach only one at a time.
- The `vxlan-rule` check loads a [worker](../reference/glossary.md#worker)'s firewall as NixOS renders it in a network namespace,
  with a peer and a pod in namespaces of their own, and sends UDP to check what the VXLAN rule
  accepts.

Prefer this level for anything about partitions, firewall rules or interfaces.

## Evaluation tests and build checks

`test/nix/eval.nix` holds evaluation tests in the form of `lib.runTests`: each `test*`
attribute has an `expr` and an `expected` value. They evaluate small clusters with `mkCluster`
and check options, assertions, what role images set and the manifest; a test that an
evaluation fails uses `builtins.tryEval`. The `eval` check fails with the list of failures.

```nix
testRoleReadsClusterEndpoint = {
  expr = (role (cluster [ ])).chalkos.cluster.endpoint;
  expected = "https://10.0.0.1:6443";
};
```

Build checks in `nix/checks.nix` check what an image build produces, without booting it:

| Check | What it checks |
| --- | --- |
| `kernel-modules` | A role's module tree holds its groups' modules and no others, and the tool refuses unknown modules and missing dependencies. |
| `image-size` | The test cluster's images stay within the ceilings in `nix/testing/image-sizes.nix`, and the fit check every image build runs refuses images that leave no room. |
| `bootspec` | A [role](../reference/glossary.md#role)'s bootspec names the [UKI](../reference/glossary.md#uki) on the ESP and no initrd. |
| `vxlan-rule` | The VXLAN firewall rule, in network namespaces. |
| `template-lab` | The [lab](../reference/glossary.md#lab) template evaluates and its images build. |
| `manifest-golden` | The homelab example's manifest equals `test/fixtures/homelab-manifest.json`. |

## End-to-end tests

The end-to-end tests in `test/e2e` boot chalkos images in QEMU with OVMF, Secure Boot and a
software TPM, through the same `pkg/lab` code [chalklab](../reference/glossary.md#chalklab) uses, and drive the nodes with the real
chalkctl. Each check runs one test or a group of them:

| Check | Test | VMs |
| --- | --- | --- |
| `e2e-firmware` | The firmware with a TPM starts and reaches the serial console. | 1 |
| `e2e-image` | The test image boots without Secure Boot. | 1 |
| `e2e-secureboot` | A signed image boots under Secure Boot and an unsigned one is refused. | 1 per test |
| `e2e-verity` | A tampered store fails to read. | 1 |
| `e2e-install` | An install in place, with a second disk. | 1 |
| `e2e-installer` | The [installer](../reference/glossary.md#installer) installs a node onto a blank disk. | 1 |
| `e2e-iso` | The installer's ISO boots. | 1 |
| `e2e-storage` | Volumes on the system disk and a data disk. | 1 |
| `e2e-upgrade` | Under Secure Boot, an [upgrade](../reference/glossary.md#upgrade) that boots from [slot](../reference/glossary.md#slot) B and is found healthy, then one that never becomes healthy and falls back. | 1 |
| `e2e-kubernetes` | A dual-stack cluster of a [control plane](../reference/glossary.md#control-plane) and a worker through chalklab: networking, DNS, renewals and [rotations](../reference/glossary.md#rotation), a reboot. | 2 |
| `e2e-kubernetes-ha` | Three control planes of an IPv6-only cluster behind a VIP: joins, failover, reinstalls and a rolling upgrade. | 3 |

A VM gets 2 CPUs and 2 GiB of memory unless its test asks for less, on a 16 GiB sparse disk.
The Kubernetes test gives its control plane 2048 MiB and its worker 1024 MiB; the HA test gives
each control plane 1536 MiB, 4.5 GiB for the three.

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
| Go code in `pkg/` or `cmd/` | `go-unit`, and the end-to-end checks covering the behaviour |
| `api/chalkos/node/v1/node.proto` | `api-generated`, `docs-generated`, `go-unit` |
| Command help, option descriptions | `docs-generated`, `docs` |
| Pages in `docs/` | `docs` |
| Modules in `modules/` or `lib/` | `eval`, `manifest-golden`, and the build and end-to-end checks of what the module does |
| Kernel modules, closure, image size | `kernel-modules`, `image-size`, `bootspec` |
| The firewall or VXLAN rule | `vxlan-rule`, `e2e-kubernetes` |
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
