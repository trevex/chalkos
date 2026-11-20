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

A pull request targets `main`, and each of its commits is a
[Conventional Commit](https://www.conventionalcommits.org/): a rebase merge puts the commits on
`main` as they are, and the changelog comes from them. Branch protection requires the `result`
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
| `fast` | the evaluation tests, linters, generated files, the docs, the Go unit tests and the namespace checks | every pull request, every push to `main` and the nightly run | one job, `fast`, of at most 90 minutes |
| `images` | the image builds and their checks, the templates, `fixtures`, and the NixOS VM checks `frr-reload` and `image-checker` | every pull request, every push to `main` and the nightly run | one job per check, of at most 120 minutes |
| `e2e` | every `e2e-*` check, the `example-*` checks, which build the examples' images that no other check shares, and the `release-image-*` checks, which build the [release images](#release-images) | every push to `main`, the nightly run, and a pull request by its labels | one job per check, of at most 180 minutes |

A job whose build fails builds once more, which rebuilds only what failed, because a download
from a module proxy or a registry fails now and then. The job's log warns of the retry.

A check in no tier, in two tiers, or a name in a tier that is no check fails the evaluation of
the checks, so a new check cannot be left out of CI. The `e2e` tier takes the checks of the
labels in `e2eLabels` of `tiers.nix`: every check whose name starts with `e2e-` or
`release-image-`, and the examples' checks. A new example check goes in the `examples` list there; a new check of another
kind goes in the `fast` or `images` list. A check goes in `fast` when it builds no image and boots
no VM, and in `images` when it builds or boots images that other checks share.

The `ci-tiers` check also wants each label of `e2eLabels` in `.github/labels.yml`, so a new
end-to-end check needs its label there too, and the label `ci-failure`.

## End-to-end labels

A pull request runs no end-to-end check unless a label asks for it:

| Label | Runs |
| --- | --- |
| `e2e` | every end-to-end check |
| `e2e:<name>` | the check `e2e-<name>`, such as `e2e:upgrade` for `e2e-upgrade` |
| `e2e:examples` | the checks of the examples: `example-homelab`, `example-extensions`, `example-bgp-extension` and `example-bgp-extension-validator-docs` |
| `e2e:release` | the `release-image-*` checks, which build the release images |

Add `e2e` when a change touches boot, the install, upgrades or a layer, `e2e:<name>` when one
check covers the change, `e2e:examples` when it touches `examples/`, and `e2e:release` when it
touches `nix/release.nix` or what the release images record. Adding any label, an end-to-end one or another, runs the workflow again:
the fast and image tiers, and the end-to-end checks the pull request's labels name. Only
people with triage access to the repository can add labels, so a pull request from a fork boots
no end-to-end VM before a maintainer has read it. The release pull request always carries `e2e`,
as [Release chalkos](releasing.md#the-release-pull-request) describes.

The workflow `.github/workflows/labels.yml` creates or updates the repository's labels from
`.github/labels.yml` when that file changes on `main`.

## Nightly run and failures

The workflow `.github/workflows/nightly.yml` runs every tier on `main` each night, unless a run
of a push already passed on `main`'s latest commit. A run started by hand always builds. When a check fails on a push to `main` or in the nightly run, CI opens an issue
labelled `ci-failure`, or comments on the open one, with a link to the run.

## Binary cache

CI caches with no setup. Every job runs [Hestia](https://github.com/Mic92/hestia), a Nix binary
cache kept in the repository's GitHub Actions cache: Nix substitutes from it what an earlier run
built, and at the end of the job Hestia uploads what the job built, in deduplicated chunks. Paths
that `cache.nixos.org` serves stay out of it.

The Actions cache holds 10 GB for the whole repository. GitHub evicts the entries used least
recently once it is full, and any entry unused for 7 days; a job rebuilds what was evicted. The
workflow `.github/workflows/cache-gc.yml` deletes each day the paths that no branch's latest run
used. A run on `main` writes to `main`'s scope, which every pull request reads; a pull request's
run writes to a scope of its own, which only that pull request reads and which goes when its
branch does, so a pull request cannot change what `main` substitutes. The cache's token lasts
about six hours, longer than any job's timeout. The cache serves CI alone; it is not reachable
from a developer's machine.

A [Cachix](https://www.cachix.org/) binary cache can serve local builds too. Every job reads it
once the repository names it in the Actions variable `CACHIX_NAME`, and runs on `main` push to
it with the secret `CACHIX_AUTH_TOKEN`. Pull requests never push to it. A developer reads it
with `cachix use <name>`.

The jobs of the [release workflow](releasing.md) run on `main` and read both caches, which only
runs on `main` write to, but write to neither. They rebuild whatever the caches miss, so a release
needs neither cache. The composite action `.github/actions/nix` installs Nix and
sets up the caches for the jobs of both workflows.

## Release images { #release-images }

`nix/release.nix` builds the images a release of chalkos publishes, the flake output
`releaseImages`. Each published Kubernetes minor of `modules/node/kubernetes/releases.nix`, the
minors with `publish = true`, and the router get one image on metal and one on KVM. The images
carry no OS CA and no Secure Boot signature, record the tag `v<version>` of `version.nix` and the
commit's revision, and annotate their manifests as
[Image layout](../reference/image-layout.md#oci-annotations) describes.

Each image is named after the repository below `ghcr.io/trevex/chalkos/` that a release pushes
it to, and carries that repository and its tag:

```sh
nix eval --json .#releaseImages.x86_64-linux \
  --apply 'builtins.mapAttrs (_: image: { inherit (image) repository tag; })'
```

```json
{
  "kubernetes-1.37-kvm": { "repository": "ghcr.io/trevex/chalkos/kubernetes-1.37-kvm", "tag": "v0.1.0" },
  "kubernetes-1.37-metal": { "repository": "ghcr.io/trevex/chalkos/kubernetes-1.37-metal", "tag": "v0.1.0" },
  "router-kvm": { "repository": "ghcr.io/trevex/chalkos/router-kvm", "tag": "v0.1.0" },
  "router-metal": { "repository": "ghcr.io/trevex/chalkos/router-metal", "tag": "v0.1.0" }
}
```

A Kubernetes image's own name, which its os-release names, leaves out the minor:
`kubernetes-metal` and `kubernetes-kvm`. `chalkctl upgrade --image` then finds the nodes of a
previous minor by the image's name. Since the names hold dots, a build quotes them:

```sh
nix build '.#releaseImages.x86_64-linux."kubernetes-1.37-kvm".oci'
```

The fast check `release-eval` evaluates every release image and holds it to its repository, tag,
provenance and annotations without building it. Each `release-image-<name>` check, with the dots
of the name as dashes, builds one image and its OCI image layout and compares the manifest's
annotations with the image's. They run in the `e2e` tier, one job each, as four full images are
too heavy for every pull request.

## Reproduce a job

Each job runs `nix build` of its checks, so a failure reproduces locally with the same command.
Build one check with its log:

```sh
nix build -L .#checks.x86_64-linux.image-oci
```

The `fast` job builds every check of its tier in one `nix build --keep-going`; the log names the
check that failed.
