---
title: "Contribute"
description: "How to contribute to chalkos: what a change needs and where to start"
---

# Contribute

These pages are for contributors who change chalkos itself. chalkos takes changes as pull requests
against `main` on [GitHub](https://github.com/trevex/chalkos). A change is ready when it carries
its tests, the documentation it affects, formatted code, and passing flake checks for what it
touches.

<div class="grid cards" markdown>

-   :material-laptop: __Development setup__

    ---

    The development shell, the repository layout and the commands of a development loop.

    [:octicons-arrow-right-24: Read](development.md)

-   :material-format-list-checks: __Common changes__

    ---

    Checklists for an API method, a chalkctl command, a built-in kind, a router feature and a
    layer.

    [:octicons-arrow-right-24: Read](common-changes.md)

-   :material-gavel: __Code conventions__

    ---

    Compatibility, layout, layers, feature toggles, refusals, comments, golden files, generated
    files and commit messages.

    [:octicons-arrow-right-24: Read](conventions.md)

-   :material-test-tube: __Testing__

    ---

    Unit, namespace, evaluation and end-to-end tests, and when to write which.

    [:octicons-arrow-right-24: Read](testing.md)

-   :material-sitemap: __Architecture for developers__

    ---

    Where Nix and Go meet, how a layer splits across the code, and where to make each kind of
    change.

    [:octicons-arrow-right-24: Read](architecture.md)

-   :material-package-variant: __Packages and programs__

    ---

    Every Go program in `cmd/` and package in `pkg/`, grouped by area.

    [:octicons-arrow-right-24: Read](packages.md)

-   :material-cog-outline: __Internals__

    ---

    The code behind the node API, layer plugins, redaction, the sandbox, the install, staging
    and storage matching.

    [:octicons-arrow-right-24: Read](internals/index.md)

-   :material-nix: __Nix code__

    ---

    How the Nix code is laid out, what a module gets, where a new helper goes and the practices
    the Nix code follows.

    [:octicons-arrow-right-24: Read](nix.md)

-   :material-file-document-edit: __Writing documentation__

    ---

    How to write, build, check and preview the documentation.

    [:octicons-arrow-right-24: Read](writing-docs.md)

-   :material-format-text: __Docs style guide__

    ---

    The rules every documentation page follows.

    [:octicons-arrow-right-24: Read](style.md)

-   :material-map-marker-path: __Roadmap and known limits__

    ---

    What chalkos does not do yet, and what is planned.

    [:octicons-arrow-right-24: Read](roadmap.md)

</div>

## Where to start

Start with [First change in 10 minutes](development.md#first-change-in-10-minutes): it takes a
small change from a fresh clone to a commit. Then read
[Architecture for developers](architecture.md) for where things live, and
[Common changes](common-changes.md) for the steps of a frequent change. The
[roadmap](roadmap.md) lists [good first changes](roadmap.md#good-first-changes), what chalkos does
not do yet and the known limits of what it does.

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
- The generated files its sources produce, and a commit message of the form the
  [code conventions](conventions.md#commit-messages) give.

