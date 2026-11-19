# Contributing to chalkos

chalkos takes changes as pull requests against `main`. This file is the short version; the
[Contribute](https://trevex.github.io/chalkos/contribute/) pages of the documentation have the
details.

## What to read

1. [Development setup](https://trevex.github.io/chalkos/contribute/development/), starting with
   its "First change in 10 minutes".
2. [Architecture for developers](https://trevex.github.io/chalkos/contribute/architecture/), for
   where the code lives.
3. [Testing](https://trevex.github.io/chalkos/contribute/testing/), for which test to write and
   which checks to run.
4. [Common changes](https://trevex.github.io/chalkos/contribute/common-changes/), for the
   steps of an API method, a command, a kind, a router feature or a layer, and
   [Code conventions](https://trevex.github.io/chalkos/contribute/conventions/).
5. The [roadmap](https://trevex.github.io/chalkos/contribute/roadmap/), for good first changes and
   what chalkos does not do yet.

## Set up

You need an x86-64 Linux machine with Nix and flakes enabled, and `/dev/kvm` for the tests that
boot virtual machines. The development shell has every other tool:

```sh
git clone https://github.com/trevex/chalkos
cd chalkos
nix develop
```

## Run the tests

Run one package's Go tests in the shell, and build the flake check of what you changed:

```sh
go test ./pkg/<pkg>/...
nix build -L .#checks.x86_64-linux.<name>
```

`nix flake check` runs every check, the end-to-end tests that boot VMs included. The
[flake checks](https://trevex.github.io/chalkos/contribute/testing/#flake-checks) table maps each
part of the repository to its checks.

## Commit

Format the code with `gofmt -l -w cmd pkg test` and `nix fmt` before you commit. Commits follow
[Conventional Commits](https://www.conventionalcommits.org/): a type, an optional scope and a
summary in the imperative, such as
`fix(chalkctl): check every node of an image's name before skipping it`. The body, when there is
one, says what changed and why. Commit messages and code comments carry no trailers and no
references to plans, tasks or milestones.

A change carries its tests, the documentation it affects and the generated files its sources
produce.
[What a change needs](https://trevex.github.io/chalkos/contribute/#what-a-change-needs) lists
each one.

## Open a pull request

Every change reaches `main` through a pull request of Conventional Commits, which a rebase merge
puts on `main` as they are. CI runs the fast and image checks on every pull request; add the
label `e2e`, or `e2e:<name>` for one check, when the change touches boot, the install, upgrades
or a layer.
[Continuous integration](https://trevex.github.io/chalkos/contribute/ci/) explains the tiers and
labels.
