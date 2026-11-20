---
title: "Code conventions"
description: "The rules chalkos's Go, Nix and proto code follow: compatibility, layout, layers, refusals, comments, generated files, commit messages and sign-off"
---

# Code conventions

This page lists the rules that the code of chalkos follows, and that reviews hold a change to. Each
rule says what to do and why. [Testing](testing.md) has the rules for tests, and the
[docs style guide](style.md) those for documentation.

## Compatibility

chalkos is before 1.0. A change does not keep the old behaviour beside the new one:

- No compatibility shims or fallbacks for older chalkd, chalkctl or images. A change to the node
  API, a kind or a file format changes every side at once, and a cluster upgrades together.
- A field removed from a proto file is deleted, without a `reserved` line. The fields of a
  message keep numbers without gaps, so the next field takes the next number.

## Layout

- New Go packages go in `pkg/`. A helper that several packages share, but that is not an API of
  its own, goes in `pkg/internal/`, as `pkg/internal/cli`, `pkg/internal/golden` and
  `pkg/internal/testwait` do.
- `cmd/` holds the mains, which wire packages up. Logic worth a test lives in `pkg/`.
- Every directory of Go code needs a row in [Packages and programs](packages.md). A directory that
  loses its Go code loses its row. The `docs` check fails otherwise.
- A new Nix helper goes where [Nix code](nix.md#where-a-new-helper-goes) says.

## Layers own their names

Common code names no layer. The node API, chalkd, chalkctl, the inventory and the identity carry
no Kubernetes or router specifics. A layer plugs in through a plugin of each program instead, as
[Layer plugins](internals/layer-plugins.md) describes.

A layer reacts to a change through its Nix effects. Its chalkd plugin adds subcommands, run as
`chalkd <layer> <command>`, and actions, named `<layer>:<action>`, and the effects call them.

## Features can be left out

A feature of a layer is on by default, and an image can leave it out. Each feature has the option
`chalkos.<layer>.<feature>.enable`, true by default and effective only with the layer, such as
`chalkos.router.dhcp.enable`. Setting it false in an image's `modules` removes everything of the
feature: its packages, units, outputs, effects, diagnostics and kinds. An image without the
feature refuses its kinds' documents.

A new feature gets its option from the start, with an evaluation test of what leaving it out
removes. [Common changes](common-changes.md#add-a-router-feature) lists the steps for the router.

## Refusals

A config that a check refuses is an error of a known type, so every program that runs the check
reports it the same way:

- A check of one document's field returns `refusals.Errorf(kind, name, path, format, args...)`
  from `pkg/refusals`. Its message says what is wrong and how to fix it. Code around the check
  passes the error on unchanged, and `refusals.Split` collects the refusals of an error.
- A stage of a delivery's check that refuses a config returns a `*check.Refusal` from
  `pkg/check`: the stage, such as `rules` or `validators`, and its message. chalkd tells a refusal
  from a failure with `errors.AsType[*check.Refusal]`.

## Comments

- Keep comments short and in the present tense. Say what the code is and why, not what changed.
- A comment explains a decision, not the next line.
- Comments and commit messages name no plan, task, step or milestone. A comment carries no
  trailer, and a commit message none but its [sign-off](#sign-off).
- The doc comment of an exported Go name starts with that name, which staticcheck checks.

## Golden files

Long output is compared with golden files instead of with strings in the test:

- New Go golden tests use `pkg/internal/golden`. `go test -run <test> -update` in the package
  rewrites the files in its `testdata/`. The router's renderers in `cmd/chalkos-render` keep a
  helper of their own with the same flag.
- Image tests compare an output's rendered files with the directories that their `outputs` name,
  such as `modules/node/router/tests/<example>/<output>`. The `image-tests` check prints the
  command that takes a new set.

Review the diff of a golden file before you commit it. [Testing](testing.md) describes both.

## Generated files are committed

Generated code and pages live in the repository, so a change that alters their sources
commits what they generate too. A check fails when they differ.

| Source | Command | Generated | Check |
| --- | --- | --- | --- |
| `api/chalkos/*/v1/*.proto` | `buf generate` | `pkg/api/*/v1/`, `docs/reference/api.md` | `api-generated`, `docs-generated` |
| Command help, option declarations, the kinds' JSON Schemas, the built-in rules, facts, outputs and effects, the proto files | `nix run .#docgen` | `docs/reference/cli/`, `docs/reference/mkimages.md`, `docs/reference/image-options.md`, `docs/reference/extension-api.md`, `docs/reference/kinds.md`, `docs/reference/builtins.md`, `docs/reference/api.md`, the navigation in `zensical.toml` | `docs-generated` |
| `docs/reference/glossary.md` | `nix run .#docgen` | `docs/includes/abbreviations.md` | `docs-generated` |
| `go.mod`, `go.sum` | `nix run .#update-vendor-hash` | `vendorHash` in `nix/overlay/build-go-program.nix` | every Go package's build |

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org/): a type, an
optional scope and a summary in the imperative, such as
`fix(chalkctl): check every node of an image's name before skipping it` or
`docs(chalklab): describe chalklab's commands`.
The body, when there is one, says what changed and why. Commit messages and code comments stand
on their own for a reviewer: they carry no references to plans, tasks or milestones, and the
only trailer of a commit message is its sign-off.

A squash merge puts a pull request on `main` as one commit, whose subject is the pull request's
title, so the title follows the same form. The `pr-title` job checks it, and
[Continuous integration](ci.md#pull-requests) shows how a pull request lists several changes for
the changelog.

## Sign-off { #sign-off }

Every commit is signed off by its author:

```sh
git commit -s -m "fix(inventory): <summary>"
```

`-s` adds the trailer `Signed-off-by: Name <email>` with the `user.name` and `user.email`
of git's configuration, which the commit's author has too. The `dco` job of CI fails on a commit
whose sign-off does not name its author exactly. Sign off the commits of a branch that lack it,
then force-push:

```sh
git rebase --signoff origin/main
git push --force-with-lease
```

With the sign-off, the author certifies the
[Developer Certificate of Origin](https://developercertificate.org/) 1.1: that they wrote the
change or have the right to submit it under the project's licence, Apache-2.0, and that the
change and the sign-off, with their name and email, stay in the project's public record.

A change written with the help of an AI tool is signed off by the person who submits it. That
person has read the change, answers for it in review, and certifies its origin as for any other
change. A commit names no tool or agent as a co-author: it carries no `Co-Authored-By` or other
trailer besides the sign-off.

## Signed commits { #signed-commits }

The ruleset on `main` accepts a pull request only when GitHub verifies the signature of every
one of its commits. Sign your commits with a GPG or SSH key that your GitHub account lists as a
signing key, as GitHub's
[commit signature verification](https://docs.github.com/en/authentication/managing-commit-signature-verification)
describes. For an SSH key, configure git once:

```sh
git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global commit.gpgsign true
```

GitHub then shows each pushed commit as "Verified". `git rebase --signoff origin/main` signs the
commits it rewrites again, so one rebase adds both the sign-off and the signature to a branch
whose commits lack them.

GitHub signs the commits it makes itself: those of its web editor, and those that the
workflows, release-please and Renovate make through its API as a GitHub App. The workflows of
[Dependency updates](dependencies.md) commit through the API for this reason, with
`.github/scripts/push-signed.sh`. A commit made in GitHub's web editor carries a sign-off only
when the repository setting "Require contributors to sign off on web-based commits" is on, which
makes the editor add it.
