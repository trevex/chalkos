---
title: "Code conventions"
description: "The rules chalkos's Go, Nix and proto code follow: compatibility, layout, layers, refusals, comments and generated files"
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
- Comments and commit messages name no plan, task, step or milestone, and carry no trailers.
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
on their own for a reviewer: they carry no trailers and no references to plans, tasks or
milestones.
