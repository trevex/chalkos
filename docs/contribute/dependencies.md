---
title: "Dependency updates"
description: "How Renovate, the fixup workflow and the weekly flake update keep the Go modules, the actions, the Kubernetes releases and the flake's inputs up to date"
---

# Dependency updates

Renovate and two workflows keep chalkos's dependencies current. Renovate opens a pull request per
group of dependencies each Monday: the Go modules, the Go release, the GitHub Actions and the
Kubernetes release of each pinned minor. The workflow `renovate-fixup.yml` then sets what Nix
derives from Renovate's change, and the workflow `update-nixpkgs.yml` updates `flake.lock`, which
Renovate leaves alone. Every update reaches `main` as a pull request that CI checks like any
other, as [Continuous integration](ci.md) describes.

## Renovate's groups

`.github/renovate.json5` configures Renovate. It runs three managers: `gomod` for `go.mod`,
`github-actions` for the workflows and `.github/actions/`, and two regular-expression managers,
for `modules/node/kubernetes/releases.nix` and for Hestia's `version` input beside its action. The
`nix` manager and every other one stay off.

Renovate works on Monday before 06:00 Europe/Berlin. Each group is one pull request on the branch
`renovate/<group>`, labelled `dependencies`. Its commits are Conventional Commits: `deps:` for what chalkos ships, the Go modules and the
Kubernetes pins, and `ci(deps):` for actions. release-please makes a patch release for `deps:`
commits and lists them under Dependencies; `ci(deps):` commits change nothing chalkos ships and
release nothing. Renovate rebases a pull
request once `main` moves past it, and lists every pending and open update on its Dependency
Dashboard, an issue it keeps open.

| Group | What it updates | Labels | Merge |
| --- | --- | --- | --- |
| `github-actions` | The actions' patch and minor releases and digests, which stay pinned by digest with the version as a comment, except those of `github-actions-release` | `dependencies` | automerge |
| `github-actions-release` | The patch and minor releases and digests of the actions that run in the release jobs holding `id-token: write`: those of the `nix` composite action and Hestia's `version` input, `sigstore/cosign-installer`, `actions/attest`, `actions/attest-build-provenance`, `actions/checkout`, `actions/upload-artifact` and `actions/download-artifact` | `dependencies` | by hand |
| `go-kubernetes` | The modules `k8s.io/*`, `sigs.k8s.io/*` and `go.etcd.io/*` | `dependencies`, `e2e` | by hand |
| `go-x` | The modules `golang.org/x/*` | `dependencies` | by hand |
| `go-deps` | Every other Go module's patch and minor releases and pseudo-versions | `dependencies` | by hand |
| `go-toolchain` | The `go` directive and the `toolchain` line of `go.mod` | `dependencies`, `e2e` | by hand |
| `kubernetes-<minor>` | The patch release of one minor in `releases.nix`, a pull request per minor | `dependencies`, `e2e` | by hand |

Renovate proposes a release of an action or a Go module only once it is seven days old,
`minimumReleaseAge` in `.github/renovate.json5`. A compromised release is usually found and
withdrawn within that time, before Renovate opens a pull request for it. A security update does
not wait, as the next section describes.

A major release of an action gets a pull request of its own, outside its group, and never merges
by itself. A major release of a Go module waits on the Dependency Dashboard under "Pending
Approval", since its API change usually needs the code ported. Tick its box to have Renovate open
a pull request with the import paths rewritten to the new major, then port the code on that
branch, or open your own pull request and close Renovate's. Indirect Go modules follow from
`go mod tidy`, which Renovate runs after each change of `go.mod`.

The Connect code generator, protoc-gen-connect-go, is not a Go module of `go.mod`: the flake
builds it from a pinned connect-go release in `nix/packages/protoc-gen-connect-go.nix`, for the
dev shell and the `api-generated` check. When Renovate updates `connectrpc.com/connect/v2`, set
the same version there, take `hash` and `vendorHash` from the errors of the next build, and run
`buf generate` in the dev shell.

The label `e2e` runs every end-to-end check on the pull request. The Kubernetes modules and the
Go release change chalkd and every program in the images, and a Kubernetes release changes every
kubelet, so their pull requests carry it from the start.

## Security updates

An update that fixes a known vulnerability does not wait for Monday. Renovate reads the
vulnerabilities of the Go modules and the actions from the repository's Dependabot alerts and from
the [OSV](https://osv.dev/) database, and opens a pull request for each fixed version at any time,
outside its group, labelled `dependencies` and `security`. Its title ends with `[SECURITY]`. It
merges like the group it leaves: an action's fix of the `github-actions` group by itself once CI
passes, every other fix by hand.
`vulnerabilityAlerts` in `.github/renovate.json5` sets this.

govulncheck in CI fails when the code calls a vulnerable function, often before Renovate's pull
request arrives. Fix it by updating the module to the fixed version the job prints, then setting
the vendor hash:

```sh
go get golang.org/x/net@v0.60.0
go mod tidy
nix run .#update-vendor-hash
nix run .#govulncheck
```

## Automerge

Only the `github-actions` group merges by itself, and only with every check green. Renovate
enables GitHub's auto-merge on the pull request, with a squash merge, so GitHub merges it once
the checks that branch protection requires pass. Where GitHub's auto-merge is unavailable, as
while the repository setting "Allow auto-merge" is off, Renovate merges the pull request itself
on a later run, once every check of the branch has passed. A pull request with a failed or
pending check, or with no check at all, stays open.

The release workflow's `image` and `assets` jobs hold `id-token: write`, which lets any step in
them sign an image or a binary as `release.yml@refs/heads/main`, the identity users verify. An
update of an action that runs in those jobs therefore goes to the `github-actions-release` group,
which a maintainer merges after reading the action's diff. When an action is added to either
job, add it to that group's `matchDepNames` too.

GitHub's auto-merge waits only for the required checks. With "Allow auto-merge" on and no
required check, GitHub merges an action's pull request at once, before CI finishes. Turn the
setting on only together with a branch protection rule or ruleset on `main` that requires the
check `result`.

## How a Kubernetes minor is tracked

The first regular-expression manager reads each row of `releases.nix` as a dependency of its own,
`kubernetes-1.37` and so on, on the GitHub releases of `kubernetes/kubernetes`. Each one takes
patch releases only: Renovate splits patch from minor updates, and the configuration turns the
minor and major updates of these dependencies off, so `kubernetes-1.36` moves to the newest
1.36 release and never to 1.37. A new minor needs no change to the configuration.

The second manager reads the first row of `releases.nix` as `kubernetes-minor`, the newest
minor, and compares it with the minors Kubernetes has released. A minor counts once it has a
`.1` release, because `update-pins` refuses an x.y.0 as the newest release. A new minor appears
on the Dependency Dashboard under "Pending Approval" and opens no pull request. Adding it is a
change by hand, as [Update a Kubernetes release](common-changes.md#update-a-kubernetes-release)
describes: add its row first in the table, remove the oldest, move `publish = true` and run
`update-pins`. Once `releases.nix` lists the new minor first, the dashboard entry goes. Approving
the entry instead opens a pull request that only renames the newest row's key, which is no
working change; close it.

The check `renovate`, in the fast tier, runs `renovate-config-validator --strict` on the
configuration and fails when `releases.nix` lists any minor before the newest.

## The fixup workflow

Renovate changes `go.mod`, `go.sum` and `releases.nix`, but not the hashes Nix derives from them,
so the builds of its pull request would fail. The workflow `.github/workflows/renovate-fixup.yml`
runs on every push to a `renovate/` branch whose latest commit is Renovate's, compares the branch
with `main`, and fixes what the change needs:

| Changed against `main` | Fix | Commit |
| --- | --- | --- |
| `go.mod` or `go.sum` | `nix flake update go-overlay` when go-overlay lacks the Go release `go.mod` names, then `nix run .#update-vendor-hash` | `build(deps): update go-overlay for Go <version>`, `build(deps): update the vendor hash` |
| `releases.nix` | `nix run .#update-pins` | `build(deps): update the Kubernetes pins` |

go-overlay adds a Go release within hours of its publication, so its update in a Go pull request
holds the release Renovate proposes. The workflow commits each fix as the release GitHub App's
bot, signed off by the bot. `.github/scripts/push-signed.sh` then makes each commit again on the
branch through GitHub's GraphQL API, `createCommitOnBranch`, with the App's token. GitHub
authors such a commit as the bot and signs it, so it passes the ruleset's
[signature check](conventions.md#signed-commits), and CI runs on it; a commit pushed with a
workflow's own token starts no workflow. A push of the bot's own commits, or
of a person's, runs nothing, so the workflow does not loop. Renovate's configuration ignores the
bot's commits in `gitIgnoredAuthors`, so Renovate still rebases the branch. A rebase replaces the
branch with a new commit of Renovate's, and the workflow fixes it again.

Renovate's commits carry no sign-off. The `dco` job of
[Continuous integration](ci.md#pull-requests) exempts them, and the bot's.

## The weekly flake update

The workflow `.github/workflows/update-nixpkgs.yml` runs each Monday at 04:00 UTC. It runs
`nix flake update`, which moves every input of `flake.lock` to its newest revision: nixpkgs,
go-overlay with the inputs it brings, and bombon, which builds the SBOMs. Updating them together
keeps go-overlay's list of Go releases current for the next Go update, and makes one pull request
a week. A change of nixpkgs
can change how Nix fetches and builds the Kubernetes sources and the Go modules, so the workflow
runs `update-pins` and `update-vendor-hash` after it.

When anything changed, the workflow commits it as `deps: update the flake inputs`, signed
off by the App's bot, and makes the commit through GitHub's API like the fixup workflow, on a
branch of the run that starts at `main`. It then moves `bot/update-flake-lock` to that commit,
replacing what the branch held, deletes the run's branch, and opens a pull request labelled
`dependencies` and `e2e`, or updates the open one. Its description lists each input's old and new
revision. A week with no change leaves the branch and the pull request alone.

A newer nixpkgs can make an image outgrow its size ceiling, and the `image-size` check then fails
the pull request. The workflow never raises a ceiling: a person decides whether to raise it in a
commit of its own on the branch or to hold the update. The next week's run replaces the branch,
so merge or carry over such a commit before then.

Start the workflow by hand with `dry-run` to list the changes in the run's summary without pushing
anything:

```sh
gh workflow run update-nixpkgs.yml -f dry-run=true
```

## The vendor hash

Every Go program of chalkos shares one download of the Go modules, a fixed-output derivation whose
hash is `vendorHash` in `nix/overlay/build-go-program.nix`. A change of `go.mod` or `go.sum`
changes the download, so the hash must change with it:

```sh
nix run .#update-vendor-hash
```

The app sets `vendorHash` to `lib.fakeHash`, builds the module download, which fails with the
hash it got, and writes that hash back. The fake hash forces the download: a download of the old
hash already in the store would otherwise pass for the new one. A second run changes nothing.
When the build fails for another reason, the app restores the old hash and prints the build's
log. It needs network access to the Go module proxy.

## Install Renovate

Renovate runs as the Mend Renovate GitHub App, which a maintainer installs once:

1. Install the [Mend Renovate App](https://github.com/apps/renovate) and give it access to
   `trevex/chalkos` alone.
2. Renovate reads `.github/renovate.json5` from `main` and runs once the file is there. Its
   first run opens the Dependency Dashboard issue, which lists the updates as awaiting the
   schedule until the next Monday morning. Tick an update's box on the dashboard to have
   Renovate open its pull request at once.
3. To let actions merge without a person, require the check `result` on `main` in a branch
   protection rule or ruleset, then turn on "Allow auto-merge" in the repository's settings. In
   that order: see [Automerge](#automerge).

The fixup and flake workflows act with the release GitHub App, from the secrets `RELEASE_APP_ID`
and `RELEASE_APP_PRIVATE_KEY`, as [Release chalkos](releasing.md#before-the-first-release)
describes. The App's bot authors their commits.
