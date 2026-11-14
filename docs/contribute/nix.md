---
title: "Nix code"
description: "How chalkos's Nix code is laid out, what a module gets, where a new helper goes and the practices the Nix code follows"
---

# Nix code

chalkos's Nix code builds chalkos's programs and images and runs its checks. This page covers
where each part lives, which arguments a chalkos NixOS module receives, where a new helper
belongs and the practices new Nix code follows. [Architecture for developers](architecture.md)
describes what the modules do.

## Where Nix code lives

| Path | What it holds |
| --- | --- |
| `flake.nix` | The flake's inputs and outputs. Each output is built in a file the flake imports. |
| `lib/` | The flake's `lib`: chalkosLib plus `mkImages` (`default.nix`). chalkosLib (`chalkos.nix`) is a function of nixpkgs's `lib` alone, with no `pkgs` and no store paths. `mkImages` (`images.nix`) evaluates a cluster's images; its arguments are declared in `images-arguments.nix`. |
| `nix/` | `overlay.nix`, which adds `pkgs.chalkos`, the scope of chalkos's packages, with one file per package; the flake's packages, apps, development shell, docs build and checks; and the test images in `testing/`. |
| `modules/` | The NixOS modules of the images: `node/` (every image, plus the Kubernetes and router layers), `platforms/`, `installer/` and `testing/`. |
| `test/nix/` | The evaluation tests, which the `eval` and `eval-kinds` checks run. |

## What a module gets

Every module of an image gets these arguments in addition to NixOS's own:

- `lib`: nixpkgs's library, unchanged.
- `chalkosLib`: chalkos's library, which holds the
  [extension API](../reference/extension-api.md#chalkoslib) and `chalkosLib.internal`.
  `lib/images.nix` passes it as a special argument, so `imports` and option declarations can use
  it. Submodules do not receive it. Code inside a module, submodules included, uses the module's
  argument, the same way it uses `pkgs` and `config`. mkImages refuses a `specialArgs.chalkosLib`
  of its caller.
- `pkgs`, with `pkgs.chalkos`: chalkos's programs, its Go toolchain `pkgs.chalkos.go`, the Go
  program builder `pkgs.chalkos.buildGoProgram` and `pkgs.chalkos.treeGoPaths`, the source paths
  of the whole Go tree.
- `routerLib`: what the router layer's parts have in common. `routerLib.types` holds the option
  types and CEL of the router kinds, and `routerLib.kinds` loads a part's kinds file.
  `modules/node/router/default.nix` sets it on every image, but it is internal to the router
  layer and not part of the extension API.
- The `specialArgs` given to mkImages.

The flake exports the overlay that adds `pkgs.chalkos` as `overlays.default`. mkImages adds it
to an image's nixpkgs with `lib.mkBefore`, so the image's own overlays can override chalkos's
packages with `pkgs.chalkos.overrideScope`.

## Where a new helper goes

1. If it needs only `lib` and belongs to no layer, add it to chalkosLib in `lib/`, under
   `internal` unless extensions should call it.
2. If it builds a derivation that the flake or an image ships, write a package file in `nix/` and
   add it to the scope in `nix/overlay.nix`. A package file takes its dependencies by name, as
   `callPackage` passes them. A file that takes an argument with the same name as a member of
   the scope gets that argument passed explicitly, as `kea.nix` gets nixpkgs's `kea`.
3. If it needs a module's `config` or belongs to one layer, add it to that layer's module
   argument, as `routerLib` is for the router layer.
4. If one module uses it, put it in a file next to that module. A script that a module builds
   from its config is a file the module calls with `pkgs.callPackage`, such as
   `modules/node/kubernetes/controlplane-ports.nix`.

Every other file below `modules/` is a NixOS module that another module lists in its `imports`,
and it takes what it needs as module arguments. The router parts' `diagnostics.nix` files are
modules too. The exceptions to this rule are:

- `router/lib.nix` and `router/types.nix`, which build `routerLib`;
- data files that a module and a flake output share, such as `kubernetes/releases.nix`;
- the router kinds files, `kinds.nix`: functions of `{ chalkosLib, lib, routerLib }` that only
  `routerLib.kinds ./kinds.nix` loads, the way `callPackage` loads a package file.

Apart from these exceptions, no module imports a file and passes it arguments by hand, and no
module imports a file of `nix/` or of another layer by path. Nothing extends `lib` with
`lib.extend`, because nixpkgs reserves `lib` for its own library.

## Practices

New Nix code follows these practices:

- Every input is a flake input, with no lookup paths such as `<nixpkgs>` and no fetch without a
  hash, so a build depends on the lock file alone.
- No import from derivation, so evaluation never waits for a build.
- A package's source is a file set (`lib.fileset`) of the paths it reads, so a change elsewhere
  rebuilds no image.
- `let … in` over `rec { }`, and `inherit (x) …` over `with x;`, so every name's origin is
  visible. A few older files still use `rec` and `with`.
- Variables, module arguments and the scope's functions, such as `buildGoProgram`, are
  lowerCamelCase. Package attributes, such as `chalkos-render`, are lowercase with dashes.
- Argument sets are sorted alphabetically.
- Every option has a type and a description, plus an example when its value is not obvious.
- A module refuses bad input with `chalkosLib.internal.refuse`, which reports every problem in
  one message, rather than with a bare `throw` or `assert`.
- A feature that a variant can leave out sets its config under `lib.mkIf` of its `enable`
  option, so a disabled feature leaves nothing in the image.
- chalkosLib holds no store paths. Anything that needs `pkgs` is a package in the scope.
- An evaluation test in `test/nix/` covers option behaviour, and a unit check covers a script. A VM
  test is for behaviour nothing smaller can show; [Testing](testing.md) explains the levels.

## Format and lint

`nix fmt` formats the Nix files. The `nix-lint` check fails on files nixfmt would change, on
unused bindings and arguments that deadnix finds, and on statix findings. `statix.toml` turns
off the `repeated_keys` lint and gives the reason.
[Development setup](development.md#format-and-lint) shows how to run the tools by hand.
