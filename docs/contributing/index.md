---
title: "Contributing"
description: "How to contribute to chalkos: what a change needs and where to start"
---

# Contributing

chalkos takes changes as pull requests against `main` on
[GitHub](https://github.com/trevex/chalkos). A change is ready when it carries the tests that
prove it at the cheapest level that can, the documentation it affects, formatted code, and
passing flake checks for what it touches. This page lists those requirements; the pages below
it explain the development loop, the tests and the code.

## Where to start

The [roadmap](roadmap.md) lists what chalkos does not do yet and the known limits of what it
does; either is a place to find work. Before changing code, read
[Architecture for developers](architecture.md) for where things live and
[Development setup](development.md) for the commands of a development loop.

## What a change needs

A change needs:

- Tests at the cheapest level that proves the behaviour: a Go unit test, a test in a network
  namespace or on a disk image file, or a Nix evaluation test. A test in a virtual machine is
  for what needs a booted node, and extends an existing end-to-end test where it can.
  [Testing](testing.md) explains which to write and the budget the VM tests keep to.
- Documentation of what the change does for a user: the command help in
  `pkg/chalkctl/help.go` or `pkg/chalklab/help.go`, an option's description in `modules/`, a
  comment in `api/chalkos/node/v1/node.proto`, and the hand-written pages it affects.
  [Writing documentation](writing-docs.md) explains how.
- Formatting: `nix fmt` formats the Nix files, and `gofmt` the Go code.
- The flake checks of what it touches, which [Testing](testing.md#flake-checks) maps to the
  parts of the repository.

## Generated files are committed

Generated code and pages live in the repository, so a change that alters their sources
commits what they generate too. A check fails when they differ.

| Source | Command | Generated | Check |
| --- | --- | --- | --- |
| `api/chalkos/node/v1/node.proto` | `buf generate` | `pkg/api/node/v1/`, `docs/reference/api.md` | `api-generated`, `docs-generated` |
| Command help, option declarations, `node.proto` | `nix run .#docgen` | `docs/reference/cli/`, `docs/reference/options.md`, `docs/reference/api.md`, the navigation in `zensical.toml` | `docs-generated` |
| `go.mod`, `go.sum` | `nix build .#chalkctl`, with `vendorHash` set to `lib.fakeHash` first | `vendorHash` in `nix/go-module.nix` | every Go package's build |
| `examples/homelab`, the cluster modules | apply the diff the check prints | `test/fixtures/homelab-manifest.json` | `manifest-golden` |

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org/): a type, an
optional scope and a summary in the imperative, such as
`fix(chalkctl): check every node of an image's role before skipping it` or
`docs(chalklab): describe chalklab's commands`.
The body, when there is one, says what changed and why. Commit messages and code comments stand
on their own for a reviewer: they carry no trailers and no references to plans, tasks or
milestones.

## The contributing pages

- [Development setup](development.md): the development shell, the repository's directories and
  the commands of a development loop.
- [Testing](testing.md): the kinds of tests, when to write which, and how to run them.
- [Architecture for developers](architecture.md): the Go packages, the Nix modules and how
  they connect.
- [Writing documentation](writing-docs.md): how the site is built, generated and checked.
- [Style guide](style.md): the rules every page follows.
- [Roadmap and known limits](roadmap.md): what chalkos does not do yet.
