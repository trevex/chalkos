---
title: "Continuous integration"
description: "How pull requests reach main, which checks and scans CI runs when, the e2e labels, the nightly run and the binary cache"
---

# Continuous integration

Every change reaches `main` through a pull request, and CI builds the flake checks of
[Testing](testing.md) on GitHub-hosted runners, which have `/dev/kvm`. The checks are the same
ones a developer builds with `nix build`; CI only decides which of them run for an event. This
page explains the tiers, the labels that run the end-to-end checks on a pull request, the nightly
run and the binary cache.

## Pull requests

A pull request targets `main`, and a squash merge puts it on `main` as one commit, which GitHub
signs. The pull request's title becomes the commit's subject and its commits' messages the body.
release-please writes the changelog from the subjects on `main`, so the title is a
[Conventional Commit](https://www.conventionalcommits.org/), such as
`fix(chalkctl): check every node of an image's name before skipping it`. Branch protection on
`main` allows squash merges alone and requires a linear history, a
[verified signature](conventions.md#signed-commits) on every commit of the pull request, and
the `result` check, which passes when every check of the run passed, a branch that is up to date
with `main`, and a review from the code owners of `.github/CODEOWNERS`. The pull request template
asks for the end-to-end labels the change needs.

## Draft pull requests

A draft pull request runs three jobs: `dco`, `pr-title` and `lint`. The `lint` job builds the
checks that `lint` of `nix/checks/tiers.nix` names, `nix-lint`, `workflows`, `renovate`,
`ci-tiers` and `go-lint`, in one `nix build`. These checks belong to the `fast` tier too, and
`tiers.nix` fails its evaluation when `lint` names a check outside that tier. No tier, no
end-to-end check, `govulncheck` or `release-pr` runs on a draft, so a branch can take many pushes
while it changes without booting a VM. Open a pull request as a draft while the change is in
progress.

On a draft, `result` fails at once with the error `Draft`, so the draft's checks show as failed
even when `dco`, `pr-title` and `lint` pass. A skipped `result` would be unsafe: branch
protection counts a skipped job as passed, and GitHub keeps the checks of a commit's earlier
runs. The skipped `result` of the draft's run would then satisfy the required check while the
run that "Ready for review" starts was still building.

"Ready for review" runs the workflow again with every job of a pull request: the tiers, the
end-to-end checks of its labels, `govulncheck` and `result`. That run cancels the draft's run if
it is still going. Until its `result` passes, the failed `result` of the draft's run holds the
merge.

"Convert to draft" runs the workflow again with the three draft jobs and the failing `result`,
which cancels the run in progress. GitHub merges no draft in any case, and the next "Ready for
review" runs every check again.

A pull request with several changes for the changelog lists them in its description, between
`BEGIN_COMMIT_OVERRIDE` and `END_COMMIT_OVERRIDE`, one Conventional Commit each:

```text
BEGIN_COMMIT_OVERRIDE
feat(chalkctl): add the flag --wait to chalkctl upgrade

fix(chalkd): keep the staged image after a failed download
END_COMMIT_OVERRIDE
```

release-please then takes these lines in place of the merged commit's message, as its
[README](https://github.com/googleapis/release-please#how-can-i-fix-release-notes) describes. The
block also corrects the changelog entry of a pull request that is already merged, as long as no
release has taken it yet.

Two jobs check every pull request, draft or ready, besides the flake checks:

| Job | Passes when |
| --- | --- |
| `pr-title` | The title is a Conventional Commit: a type of `feat`, `fix`, `docs`, `style`, `ci`, `build`, `test`, `refactor`, `perf`, `chore` or `revert`, an optional scope in parentheses, an optional `!` for a breaking change, a colon and a summary. |
| `dco` | Every commit except a merge carries a `Signed-off-by` trailer that names its author, name and email, as [Sign-off](conventions.md#sign-off) describes. The commits of Renovate and of the release GitHub App's bot are exempt. |

Both run `.github/scripts/pr-title.sh` and `.github/scripts/dco.sh`, which the `workflows`
check tests. A failed job prints how to fix it: retitle the pull request, or sign off its
commits with `git rebase --signoff origin/main` and force-push. Editing the pull request's title
or description runs the workflow again; the new run cancels the older one and substitutes from
the cache what that run built.

On the release pull request alone, once it is ready, the job `release-pr` checks that it holds one commit, made by
release-please, on the tip of `main`.
[Merge and publish](releasing.md#merge-and-publish) explains why.

## Tiers

`nix/checks/tiers.nix` puts every flake check in one of three tiers, and the workflow
`.github/workflows/ci.yml` reads them from the flake output `ciTiers`:

```sh
nix eval --json .#ciTiers.x86_64-linux
```

| Tier | Checks | Runs on | Jobs |
| --- | --- | --- | --- |
| `fast` | the evaluation tests, linters, generated files, the docs, the Go unit tests and the namespace checks | every ready pull request, every push to `main` and the nightly run | one job, `fast`, of at most 90 minutes |
| `images` | the image builds and their checks, the templates, `fixtures`, and the NixOS VM checks `frr-reload` and `image-checker` | every ready pull request, every push to `main` and the nightly run | one job per check, of at most 120 minutes |
| `e2e` | every `e2e-*` check, the `example-*` checks, which build the examples' images that no other check shares, and the `release-image-*` checks, which build the [release images](#release-images) | the nightly run, and a ready pull request by its labels | one job per check, of at most 180 minutes |

A push to `main` runs the `fast` and `images` tiers and `govulncheck`, and no end-to-end check:
the pull request ran the end-to-end checks its labels name before the merge, and the nightly run
runs them all on `main`.

A job whose build fails builds once more, which rebuilds only what failed, because a download
from a module proxy or a registry fails now and then. The retry also reruns a test that failed,
such as one of `go-unit`, so a job that passes on its retry may hide a flaky test. Such a job
warns that it passed only on its retry, and the run's summary names the checks whose builds
failed first and the `--- FAIL:` lines of their Go tests. A first attempt whose log names no
failed test failed on a download.

A check in no tier, in two tiers, or a name in a tier that is no check fails the evaluation of
the checks, so a new check cannot be left out of CI. The `e2e` tier takes the checks of the
labels in `e2eLabels` of `tiers.nix`: every check whose name starts with `e2e-` or
`release-image-`, and the examples' checks. A new example check goes in the `examples` list there; a new check of another
kind goes in the `fast` or `images` list. A check goes in `fast` when it builds no image and boots
no VM, and in `images` when it builds or boots images that other checks share.

The `ci-tiers` check also wants each label of `e2eLabels` in `.github/labels.yml`, so a new
end-to-end check needs its label there too, and the label `ci-failure`.
Every check other than the `release-image-<name>` checks must be named in code font on this page
or on [Testing](testing.md), and every end-to-end test file and test helper package on Testing.

## End-to-end labels

A ready pull request runs no end-to-end check unless a label asks for it, and a draft runs none:

| Label | Runs |
| --- | --- |
| `e2e` | every end-to-end check |
| `e2e:<name>` | the check `e2e-<name>`, such as `e2e:upgrade` for `e2e-upgrade` |
| `e2e:examples` | the checks of the examples: `example-homelab`, `example-extensions`, `example-bgp-extension` and `example-bgp-extension-validator-docs` |
| `e2e:release` | the `release-image-*` checks, which build the release images |

Add `e2e` when a change touches boot, the install, upgrades or a layer, `e2e:<name>` when one
check covers the change, `e2e:examples` when it touches `examples/`, and `e2e:release` when it
touches `nix/release.nix` or what the release images record. Adding any label, an end-to-end one
or another, runs the workflow again: on a ready pull request the fast and image tiers and the
end-to-end checks its labels name, on a draft the three draft jobs. Only
people with triage access to the repository can add labels, so a pull request from a fork boots
no end-to-end VM before a maintainer has read it. The release pull request always carries `e2e`,
as [Release chalkos](releasing.md#the-release-pull-request) describes.

Renovate's pull requests and the weekly update of `flake.lock` carry their labels from the
start, as [Dependency updates](dependencies.md) describes.

The workflow `.github/workflows/labels.yml` creates or updates the repository's labels from
`.github/labels.yml` when that file changes on `main`.

## govulncheck

The job `govulncheck` runs on every ready pull request, every push to `main` and in the nightly run,
beside the tiers, and feeds `result`. It runs `nix run .#govulncheck`, which checks the Go code
with govulncheck, built with the Go release of `go.mod`. The job fails when the code calls a
function with a known vulnerability. A vulnerability in a module that chalkos requires but never
calls is listed and passes.

govulncheck reads the Go vulnerability database from `vuln.go.dev` and the modules from the module
proxy, so it runs outside the flake's checks, whose builds have no network. The database changes
every day, so the nightly run finds an advisory published after a commit reached `main`.
Run the same check in a working tree:

```sh
nix run .#govulncheck
```

[Security updates](dependencies.md#security-updates) describes the fix.

## Nightly run and failures

The workflow `.github/workflows/nightly.yml` runs every tier on `main` each night, the
end-to-end checks included, which no push to `main` runs. A scheduled run skips CI when an
earlier nightly run already passed it on `main`'s latest commit, as after a day without a merge:
the run looks for a nightly run of that commit whose job `ci / result` passed. A push run cannot
stand in for it, since it runs no end-to-end check. A run started by hand always builds:

```sh
gh workflow run nightly.yml
```

When a check fails on a push to `main` or in the nightly run, CI opens an issue labelled
`ci-failure`, or comments on the open one, with a link to the run.

When a check passes there only on its retry, CI opens an issue labelled `flaky`, or comments on
the open one, with the summary of what failed first and a link to the run. Each comment names a
test to fix or a download that failed; close the issue once the flaky tests it names are fixed.

The nightly run's job `vulnix` runs every night, whether or not the tiers run. It builds the NixOS
system of each release image, which the caches usually hold from the `release-image-*` checks,
and matches the packages of its runtime closure against the CVEs of the National Vulnerability
Database with [vulnix](https://github.com/nix-community/vulnix). The run's summary lists the
number of matches per image and each matched package with its CVEs and highest CVSS v3 score,
and the artifact `vulnix` holds vulnix's JSON report per image. The job fails only when vulnix
cannot scan: a CVE in a nixpkgs package is fixed by the weekly update of nixpkgs, as
[The weekly flake update](dependencies.md#the-weekly-flake-update) describes. The job
`vulnix-issue` then reads the reports and opens an issue labelled `security` for the matches with
a CVSS v3 score of 7 or more, or comments on the open one when a match is new to it, as
[Security](../get-started/security.md#how-chalkos-scans-for-vulnerabilities) describes. The job
holds `issues: write` and runs no Nix action, unlike the scan. `.github/scripts/vulnix-issue.sh`
finds the new matches, and `.github/scripts/test.sh` tests it.

vulnix matches packages by name and version, so a match may not apply, for example when nixpkgs
patches the fix into a package without changing its version. Add such a match to
`.github/vulnix-whitelist.toml`, with the CVE and the reason, and the next report leaves it out.
Scan one image on a workstation as the job does:

```sh
nix build -o system .#releaseImages.x86_64-linux.router-kvm.nixos.config.system.build.toplevel
nix run --inputs-from . nixpkgs#vulnix -- --closure --whitelist .github/vulnix-whitelist.toml system
```

## OpenSSF Scorecard

The workflow `.github/workflows/scorecard.yml` runs the
[OpenSSF Scorecard](https://github.com/ossf/scorecard) checks each Monday, on each push to `main`
and when a branch protection rule changes. It publishes the results to `api.scorecard.dev`, which
the README's badge reads, and uploads them to the repository's code scanning alerts. The API
accepts results only from a workflow within
[its restrictions](https://github.com/ossf/scorecard-action#workflow-restrictions): no `env` or
`defaults`, no write permission at the workflow's level, and in the job only the actions it
approves, so keep any other step out of the workflow.

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

<!-- x-release-please-start-version -->
```json
{
  "kubernetes-1.37-kvm": { "repository": "ghcr.io/trevex/chalkos/kubernetes-1.37-kvm", "tag": "v0.8.0" },
  "kubernetes-1.37-metal": { "repository": "ghcr.io/trevex/chalkos/kubernetes-1.37-metal", "tag": "v0.8.0" },
  "router-kvm": { "repository": "ghcr.io/trevex/chalkos/router-kvm", "tag": "v0.8.0" },
  "router-metal": { "repository": "ghcr.io/trevex/chalkos/router-metal", "tag": "v0.8.0" }
}
```
<!-- x-release-please-end -->

A Kubernetes image's own name, which its os-release names, leaves out the minor:
`kubernetes-metal` and `kubernetes-kvm`. `chalkctl upgrade --image` then finds the nodes of a
previous minor by the image's name. Since the names hold dots, a build quotes them:

```sh
nix build '.#releaseImages.x86_64-linux."kubernetes-1.37-kvm".oci'
```

The fast check `release-eval` evaluates every release image and holds it to its repository, tag,
provenance and annotations without building it. Each `release-image-<name>` check, with the dots
of the name as dashes, builds one image and its OCI image layout and compares the manifest's
annotations with the image's. It also builds the image's SBOM, `releaseImages.<name>.sbom`, and
validates it as the `sbom` check does. The check of a KVM image also builds its containerDisk,
`releaseImages.<name>.containerDisk`, which a release pushes to `<name>-disk`, and checks its
layout, the digests of its blobs, its one layer of `disk/chalkos.qcow2` owned by 107:107, the
qcow2's virtual size against the system region's, from `chalkos.disk` and from the built layout,
and that its annotations are the image's with the image's name and the root hash of its header.
They run in the `e2e` tier, one job each, as four full images are too heavy for every pull
request.

Each release image's `sbom` attribute builds its SPDX 2.3 SBOM, the release asset
`<name>.spdx.json`. It is the image's own `sbom`, which every image of `mkImage` has, named after
the repository and created at the commit's time. `chalkctl.sbom` and `chalkbuild.sbom` build the CycloneDX SBOMs of the
programs. `nix/sbom.nix` builds them all within Nix, so CI, a dry run and a local build write the
same files:

```sh
nix build -o router-kvm.spdx.json .#releaseImages.x86_64-linux.router-kvm.sbom
nix build -o chalkctl.cdx.json .#chalkctl.sbom
```

[bombon](https://github.com/nikstur/bombon) reads the components of the image's NixOS system and
its kernel with their nixpkgs metadata, and `nix/sbom-spdx.jq` writes them as SPDX. bombon finds
a package only through attributes that hold derivations, so `nix/sbom.nix` adds the packages that
the system's units and files name in strings, by name from the image's nixpkgs.
[cyclonedx-gomod](https://github.com/CycloneDX/cyclonedx-gomod) reads a program's modules from the
build information in its binary, and their licences from the shared module download. The `sbom`
check, in the images tier, builds the SBOMs of the router test image and of both programs and
validates them: the image's against SPDX 2.3 with `pyspdxtools`, against the SPDX licence list,
with a version for every package, a licence for at least three quarters of them and `linux` under
`GPL-2.0-only`; the programs' with a licence for every module.

## Reproduce a job

Each job runs `nix build` of its checks, so a failure reproduces locally with the same command.
Build one check with its log:

```sh
nix build -L .#checks.x86_64-linux.image-oci
```

The `fast` job builds every check of its tier in one `nix build --keep-going`; the log names the
check that failed.
