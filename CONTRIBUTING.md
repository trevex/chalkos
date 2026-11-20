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
one, says what changed and why. Code comments and commit messages name no plan, task or
milestone.

Sign off every commit with `git commit -s`. The sign-off adds the trailer
`Signed-off-by: Your Name <you@example.com>`, which must match the commit's author. With it you
certify the [Developer Certificate of Origin](https://developercertificate.org/): you wrote the
change, or have the right to submit it under the project's licence, and you agree that it and
your sign-off are kept in the project's public record. The sign-off is the only trailer a commit
carries, so leave out `Co-Authored-By` and the like. To sign off the commits of a branch after
the fact, run `git rebase --signoff origin/main` and force-push.

You may write a change with the help of an AI tool. The person who signs it off answers for it:
they have read and understood it, and they certify its origin like that of any other change.

A change carries its tests, the documentation it affects and the generated files its sources
produce.
[What a change needs](https://trevex.github.io/chalkos/contribute/#what-a-change-needs) lists
each one.

## Open a pull request

Every change reaches `main` through a pull request, which a squash merge puts on `main` as one
commit. The pull request's title becomes that commit's subject, and release-please writes the
changelog from it, so the title is a Conventional Commit too. For a pull request with more than
one change for the changelog, add a `BEGIN_COMMIT_OVERRIDE` block to its description, with one
Conventional Commit per change, as
[Continuous integration](https://trevex.github.io/chalkos/contribute/ci/#pull-requests)
shows.

CI checks the title and every commit's sign-off, and runs the fast and image checks on every pull
request. Add the label `e2e`, or `e2e:<name>` for one check, when the change touches boot, the
install, upgrades or a layer, and `e2e:examples` when it touches `examples/`.
[Continuous integration](https://trevex.github.io/chalkos/contribute/ci/) explains the tiers and
labels.

## Licence

chalkos is licensed under the [Apache License 2.0](LICENSE). A contribution is licensed under the
same terms.
