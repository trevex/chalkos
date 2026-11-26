---
title: "Contribute"
description: "How to contribute to chalkos: what a change needs and where to start"
---

# Contribute

These pages are for contributors who change chalkos itself. chalkos takes changes as pull requests
against `main` on [GitHub](https://github.com/trevex/chalkos). A change is ready when it carries
its tests, the documentation it affects, formatted code, and passing flake checks for what it
touches.

## Read in this order

1. [First change in 10 minutes](development.md#first-change-in-10-minutes) takes a small change
   from a fresh clone to a commit.
2. [Architecture for developers](architecture.md) says where things live.
3. [Common changes](common-changes.md) gives the steps of a frequent change.
4. [Testing](testing.md) says which tests a change needs.

The [roadmap](roadmap.md) lists [good first changes](roadmap.md#good-first-changes), what chalkos
does not do yet and the known limits of what it does.

## Start

These pages take a first change from setup to commit.

<!-- docgen cards: development.md common-changes.md conventions.md testing.md -->
<div class="grid cards" markdown>

-   __[Development setup](development.md)__

    ---

    The development shell, the repository layout and the commands of a development loop

-   __[Common changes](common-changes.md)__

    ---

    Checklists for adding an API method, a chalkctl command, a built-in kind, a router feature or a layer, and for updating a Kubernetes release

-   __[Code conventions](conventions.md)__

    ---

    The rules chalkos's Go, Nix and proto code follow: compatibility, layout, layers, refusals, comments, generated files, commit messages and sign-off

-   __[Testing](testing.md)__

    ---

    Unit, namespace, evaluation and end-to-end tests, and when to write which

</div>
<!-- end docgen cards -->

## Maintain

These pages are for maintainers: continuous integration, releases and dependency updates.

<!-- docgen cards: ci.md releasing.md dependencies.md -->
<div class="grid cards" markdown>

-   __[Continuous integration](ci.md)__

    ---

    How pull requests reach main, which checks and scans CI runs when, the e2e labels, the nightly run and the binary cache

-   __[Release chalkos](releasing.md)__

    ---

    How the release pull request, the release workflow and its dry run make and publish a release of chalkos

-   __[Dependency updates](dependencies.md)__

    ---

    How Renovate, the fixup workflow and the weekly flake update keep the Go modules, the actions, the Kubernetes releases and the flake's inputs up to date

</div>
<!-- end docgen cards -->

## Code

These pages map the code, its packages and its internals.

<!-- docgen cards: architecture.md packages.md internals/index.md nix.md -->
<div class="grid cards" markdown>

-   __[Architecture for developers](architecture.md)__

    ---

    Where Nix and Go meet, how a layer splits across the code, and where to make each kind of change

-   __[Packages and programs](packages.md)__

    ---

    Every Go program in cmd and package in pkg, grouped by area, with what each one does

-   __[Internals](internals/index.md)__

    ---

    Where the code carries out each mechanism that How chalkos works explains

-   __[Nix code](nix.md)__

    ---

    How chalkos's Nix code is laid out, what a module gets, where a new helper goes and the practices the Nix code follows

</div>
<!-- end docgen cards -->

## Docs

These pages explain how the documentation is built and written.

<!-- docgen cards: writing-docs.md style.md slides.md -->
<div class="grid cards" markdown>

-   __[Writing documentation](writing-docs.md)__

    ---

    How to write, build, check and preview the documentation

-   __[Docs style guide](style.md)__

    ---

    How chalkos documentation is written: readers, voice, examples, diagrams, terms and page templates

-   __[Talks and slides](slides.md)__

    ---

    The slide decks contributed to chalkos, where the site serves them and how to add one

</div>
<!-- end docgen cards -->

## Plans

This page lists what chalkos does not do yet.

<!-- docgen cards: roadmap.md -->
<div class="grid cards" markdown>

-   __[Roadmap and known limits](roadmap.md)__

    ---

    What chalkos does not do yet, and what is planned

</div>
<!-- end docgen cards -->

## What a change needs

A change needs:

- Tests at the cheapest level that proves the behaviour: a Go unit test, a test in a network
  namespace or on a disk image file, or a Nix evaluation test. A test in a virtual machine is
  for what needs a booted node, and extends an existing end-to-end test where it can.
  [Testing](testing.md) explains which to write and the budget the VM tests keep to.
- Documentation of what the change does for a user: the command help in
  `pkg/chalkctl/help.go`, `pkg/chalkctl/layers/*/help.go`, `pkg/chalklab/help.go` or
  `pkg/chalkbuild/help.go`, an option's description in `modules/`, a comment in a proto file
  below `api/chalkos/`, and the hand-written pages it affects.
  [Writing documentation](writing-docs.md) explains how.
- Formatting and linting: `nix fmt` formats the Nix files, `gofmt` the Go code, and go vet and
  staticcheck lint the Go code.
- The flake checks of what it touches, which [Testing](testing.md#flake-checks) maps to the
  parts of the repository.
- The generated files its sources produce, and commits of the form the
  [code conventions](conventions.md#commit-messages) give, each with its author's
  [sign-off](conventions.md#sign-off).
- A pull request whose title is a Conventional Commit and whose CI run passes, with the
  end-to-end labels the change needs, which [Continuous integration](ci.md#end-to-end-labels)
  lists.

