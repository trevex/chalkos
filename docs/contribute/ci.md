---
title: "Continuous integration"
description: "How pull requests reach main, which checks CI runs when, the e2e labels, the nightly run and the binary cache"
---

# Continuous integration

Every change reaches `main` through a pull request, and CI builds the flake checks of
[Testing](testing.md) on GitHub-hosted runners, which have `/dev/kvm`. The checks are the same
ones a developer builds with `nix build`; CI only decides which of them run for an event. This
page explains the tiers, the labels that run the end-to-end checks on a pull request, the nightly
run and the binary cache.

## Pull requests

A pull request targets `main`, and its title is a
[Conventional Commit](https://www.conventionalcommits.org/), because a rebase merge keeps the
commits on `main` and the changelog comes from them. Branch protection requires the `result`
check, which passes when every check of the run passed, and a review from the code owners of
`.github/CODEOWNERS`. The pull request template asks for the end-to-end labels the change needs.

## Tiers

`nix/checks/tiers.nix` puts every flake check in one of three tiers, and the workflow
`.github/workflows/ci.yml` reads them from the flake output `ciTiers`:

```sh
nix eval --json .#ciTiers.x86_64-linux
```

| Tier | Checks | Runs on | Jobs |
| --- | --- | --- | --- |
| `fast` | the evaluation tests, linters, generated files, the docs, the Go unit tests and the namespace checks | every pull request and push to `main` | one job, `fast` |
| `images` | the image builds and their checks, the templates and examples, `fixtures`, and the NixOS VM checks `frr-reload` and `image-checker` | every pull request and push to `main` | one job per check |
| `e2e` | every `e2e-*` check | every push to `main`, the nightly run, and a pull request by its labels | one job per check |

A check in no tier, in two tiers, or a name in a tier that is no check fails the evaluation of
the checks, so a new check cannot be left out of CI. The `e2e` tier takes every check whose name
starts with `e2e-`; a new check of another tier goes in the `fast` or `images` list of
`tiers.nix`. A check goes in `fast` when it builds no image and boots no VM, and in `images`
otherwise.

The `ci-tiers` check also wants a label in `.github/labels.yml` for each end-to-end check, so a
new end-to-end check needs its label there too.

## End-to-end labels

A pull request runs no end-to-end check unless a label asks for it:

| Label | Runs |
| --- | --- |
| `e2e` | every end-to-end check |
| `e2e:<name>` | the check `e2e-<name>`, such as `e2e:upgrade` for `e2e-upgrade` |

Add `e2e` when a change touches boot, the install, upgrades or a layer, and `e2e:<name>` when one
check covers the change. Adding a label runs the workflow again with the checks it names. Only
people with triage access to the repository can add labels, so a pull request from a fork boots
no end-to-end VM before a maintainer has read it.

The workflow `.github/workflows/labels.yml` creates or updates the repository's labels from
`.github/labels.yml` when that file changes on `main`.

## Nightly run and failures

The workflow `.github/workflows/nightly.yml` runs every tier on `main` each night, and can be
started by hand. When a check fails on a push to `main` or in the nightly run, CI opens an issue
labelled `ci-failure`, or comments on the open one, with a link to the run.

## Binary cache

The jobs use a [Cachix](https://www.cachix.org/) binary cache once the repository names it in
the Actions variable `CACHIX_NAME`. A run pushes what it builds when it has the secret
`CACHIX_AUTH_TOKEN`; a pull request from a fork gets no secrets, so it reads the cache and
pushes nothing. Without the variable, every job builds from source and the substitutes of
`cache.nixos.org`.

The same cache speeds up local builds:

```sh
cachix use chalkos
```

## Reproduce a job

Each job runs `nix build` of its checks, so a failure reproduces locally with the same command.
Build one check with its log:

```sh
nix build -L .#checks.x86_64-linux.image-oci
```

The `fast` job builds every check of its tier in one `nix build --keep-going`; the log names the
check that failed.
