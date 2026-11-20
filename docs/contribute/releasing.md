---
title: "Release chalkos"
description: "How the release pull request, the release workflow and its dry run make and publish a release of chalkos"
---

# Release chalkos

A release of chalkos starts as a pull request that release-please keeps open, and ends with a
tag, a GitHub release, signed images in the GitHub Container Registry and signed downloads. This
page explains each step, the dry run that tries the build without publishing, and what the
repository needs before the first release. [Releases](../get-started/releases.md) describes what a
release publishes from a user's side.

## The release pull request

The workflow `.github/workflows/release.yml` runs
[release-please](https://github.com/googleapis/release-please) on every push to `main`. It reads
the Conventional Commits since the last release and opens or updates the release pull request,
titled `chore(main): release <version>`. The pull request sets the version, adds the release's
section to `CHANGELOG.md`, and changes every line that `release-please-config.json` lists:

| File | What changes |
| --- | --- |
| `version.nix` | The version, which the programs and the release images record. |
| The `flake.nix` of each template and example | The tag in the URL of the `chalkos` input, `github:trevex/chalkos/v<version>`. |
| `README.md` and the pages of the quick starts, the cluster guide and [Releases](../get-started/releases.md) | The tag in the commands and files that name a release. |

A line changes when it holds the marker `x-release-please-version`, or when it lies between the
markers `x-release-please-start-version` and `x-release-please-end`. In Markdown the markers are
HTML comments around a code block. A new file that names the release's tag needs the markers and
a line in `extra-files` of `release-please-config.json`.

Before 1.0, a `feat` commit or a breaking change raises the minor version and a `fix` the patch
version (`bump-minor-pre-major`). Only `feat`, `fix`, `perf` and `revert` commits and breaking
changes appear in the changelog, and a release needs at least one of them.

release-please labels the pull request `autorelease: pending` and `e2e`. The label `e2e` makes CI
run every end-to-end check on it, as [Continuous integration](ci.md#end-to-end-labels) describes.
release-please finds its pull request by both labels, so keep them on it. The `result` check and a
code owner's review then gate the merge as on any pull request.

## Merge and publish

Merge the release pull request with a rebase merge, like any other. The push to `main` runs the
release workflow again: release-please tags the merge commit `v<version>`, creates the GitHub
release with the changelog as its notes, and relabels the pull request `autorelease: tagged`.
The same run then publishes the release from the tagged commit:

| Job | What it does |
| --- | --- |
| `plan` | Lists the images of the flake output `releaseImages` and fails when their tag is not the release's. |
| `image <name>` | One job per image: builds its OCI layout, pushes it with `chalkbuild push` to its repository and tag, signs the digest with `cosign sign` and attests its provenance in the registry. |
| `assets` | Builds the generic installer, chalkctl and chalkbuild, compresses the installer's raw image, writes `SHA256SUMS`, signs every file with `cosign sign-blob`, attests their provenance and uploads them to the release. |
| `notes` | Appends the images' references by digest and the commands that verify them to the release's notes. |

Signing is keyless: cosign and the attestations use a certificate that Sigstore issues to the
workflow run, `release.yml` on `refs/heads/main`, so the repository holds no signing key. The
identity is the branch rather than the tag, because the run that publishes is the one that the
merge to `main` starts.

One release workflow runs at a time, and a run that publishes is never cancelled. When a job
fails, use "Re-run failed jobs" on the run: it builds the same commit, pushes the same digests,
uploads the assets again over the old ones and replaces the section of the notes it added
before.

The jobs substitute from the caches what runs on `main` built, and build the rest. They write
nothing to the caches.

## Try a release with a dry run

A dry run builds every image and asset of a ref, and publishes nothing:

```sh
gh workflow run release.yml --ref <branch> -f dry-run=true
```

It runs the jobs of a release from `plan` on, at the ref's commit, without release-please and
without any step that pushes, signs, attests or uploads. The run's summary lists each image's
reference with the digest it would push, and each asset with its size and checksum. The assets
are the run's artifact `release-assets`, kept for seven days. Run one after a change to
`nix/release.nix`, to the installer or to the workflow, before it reaches a release.

A dispatched run with `dry-run=false` does what a push to `main` does, and fails on any other
branch.

## Before the first release

These settings live in GitHub, not in the repository, so a maintainer makes them once:

1. Create a GitHub App owned by the repository's owner, with no webhook and these repository
   permissions: Contents, Pull requests and Issues, each read and write. Install it on
   `trevex/chalkos` alone. release-please acts with the App's token, because a pull request, a
   label or a tag that a workflow's own token creates starts no workflow, and the release pull
   request must run CI. The workflows of [Dependency updates](dependencies.md) use the same App
   for the same reason.
2. Add two repository secrets: `RELEASE_APP_ID`, the App's client ID, and
   `RELEASE_APP_PRIVATE_KEY`, a private key generated for it in PEM form. Without them the release
   workflow fails on every push to `main` with the error `No release App`.
3. Merge the first release pull request, then make each package of the release public: the
   GitHub Container Registry creates a package, such as `chalkos/kubernetes-1.37-metal`, private on
   its first push. Set its visibility to public in the package's settings. A later release that
   adds a Kubernetes minor or a platform adds a package, which needs the same.

The first release is v0.1.0: `.release-please-manifest.json` holds `0.0.0`, which counts as no
release, and `initial-version` in `release-please-config.json` sets the version of the first one.
`bootstrap-sha` limits the first changelog to the commits after the release workflow was added.
release-please reads neither once a release exists, so both may be removed afterwards.

## Check that it worked

The GitHub release lists the installer, the two programs, `SHA256SUMS` and a `.sigstore.json`
bundle for each, and its notes end with the images' references. `cosign verify` and
`gh attestation verify` accept each image and asset, as
[Releases](../get-started/releases.md#verify-an-image) shows, and an anonymous
`docker manifest inspect` or `crane manifest` of each image succeeds once its package is public.
