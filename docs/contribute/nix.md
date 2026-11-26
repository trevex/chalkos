---
title: "Nix code"
description: "How chalkos's Nix code is laid out, what a module gets, where a new helper goes and the practices the Nix code follows"
---

# Nix code

chalkos's Nix code builds chalkos's programs and images and runs its checks. This page covers
where each part lives, what the modules hold, which arguments a chalkos NixOS module receives,
where a new helper belongs and the practices new Nix code follows.
[Architecture for developers](architecture.md) shows where the Nix and Go halves meet.

## Where Nix code lives

The repository's top level holds what chalkos is and what its users use: the Go code, `modules/`,
`lib/`, `templates/`, `examples/`, `docs/` and `test/`. `nix/` holds how the repository builds
and checks it.

| Path | What it holds |
| --- | --- |
| `flake.nix` | The flake's inputs and outputs. Each output is built in a file the flake imports. |
| `version.nix` | chalkos's version, the one place it is written: the flake's `lib.version`, the version of every Go program and the tag of the release images come from it. release-please changes it on a release, on the line its `x-release-please-version` comment marks. |
| `lib/` | The flake's `lib`: chalkosLib plus `mkImage`, `mkInstaller`, `mkImages` and their helpers (`default.nix`), and the layers, which the flake exports as `nixosModules` (`layers.nix`). chalkosLib (`chalkos.nix`) is a function of nixpkgs's `lib` alone, with no `pkgs` and no store paths. `mkImage` (`images.nix`) evaluates one image and `mkImages` a cluster's; their arguments are declared in `images-arguments.nix`. |
| `modules/` | The NixOS modules of the images: `node/` (every image, plus the modules of the Kubernetes and router layers, which `nixosModules.kubernetes` and `nixosModules.router` enable), `platforms/`, `installer/` and `testing/`. |
| `test/nix/` | The evaluation tests, which the `eval` and `eval-kinds` checks run. |
| `nix/` | How the repository builds and checks chalkos, one entry per kind of file. |

`nix/` holds these entries:

| Path | What it holds |
| --- | --- |
| `nix/overlay/` | `default.nix`, the overlay that adds `pkgs.chalkos`, the scope of chalkos's packages, and the scope's helpers: `build-go-program.nix` (`buildGoProgram`), `go-paths.nix` (`treeGoPaths`) and `trim-package.nix` (`trimPackage`). |
| `nix/packages/` | The scope's packages, one file each, named as the package. The overlay finds them with `lib.packagesFromDirectoryRecursive`. |
| `nix/outputs/` | The flake outputs that one file builds: `packages.nix`, `apps.nix` and `shell.nix`, the development shell. |
| `nix/release.nix` | The images a release publishes, the flake output `releaseImages`. [Continuous integration](ci.md#release-images) describes them. |
| `nix/sbom.nix`, `nix/sbom-spdx.jq` | The SBOMs of the images, which `mkImage` attaches as `sbom`, and of chalkctl and chalkbuild, and the conversion of bombon's CycloneDX to SPDX. |
| `nix/docs/` | The docs output: `default.nix` builds the site and the generated reference pages, `nav.py` checks the site's navigation, and `templates/` holds the openings of the options pages and the end of the extension API's. |
| `nix/checks/` | The flake's checks. |
| `nix/testing/` | The test images, their fixtures and the test environment that the checks, the apps and the development shell share. |

## What the modules and lib hold

| Directory | What it holds |
| --- | --- |
| `modules/node/` | The NixOS modules of every image: `base/` (appliance, closure, diagnostics, disk, image directory, storage), `kinds/` (`chalkos.kinds`, `chalkos.facts`, networkd's kinds and the kinds every image accepts), `runtime/` (grants, `chalkos.outputs`, `chalkos.validators`, `chalkos.effects`, `chalkos.tests`, chalkcfg on the system path), `kubernetes/` (the kubelet, its kinds, effects, the CNI, containerd, registries, firewall), `router/` (the router layer), `frr/` (FRR's unit, user, credentials fill, output check and diagnostics, which a layer's BGP feature turns on through the internal options `chalkos.frr`), and a file each for the kernel, chalkd, the console, the kernel's consoles, diagnostics, the network, time, the layers' markers and upgrades. |
| `modules/node/router/` | The router layer: `default.nix` (its option, marker and forwarding), the Router kind (`kinds.nix`) and its output `router-sysctl` (`sysctl.nix`), `lib.nix`, traceroute (`diagnostics.nix`) and the validator router (`validator.nix`). `lib.nix` holds what the router's parts share, which they get as the module argument `routerLib`: the enable option of a feature, the kinds the image declares, the sandbox of the units, the loader of a part's kinds file and, as `routerLib.types`, the types the router kinds share from `types.nix`. `routerLib` belongs to the router layer and is not part of the extension API, although every image's modules can read it. |
| `modules/platforms/` | `metal` and `kvm`. |
| `modules/installer/` | The installer image and its ISO. |
| `modules/testing/` | The test probe, the test image's partition sizes and the test-only config kind `Greeting` with its output and effect. |
| `lib/` | `mkImage`, `mkInstaller`, `mkImages` and the helpers over their result (`images.nix`); their arguments as module options (`images-arguments.nix`); the layers, which the flake exports as `nixosModules` (`layers.nix`); and `chalkos.nix`, which builds `chalkosLib`. `chalkosLib` is the module argument of every image's modules and, beside `mkImages`, the flake's `lib`. The functions the modules share are reached through `chalkosLib.internal` alone: `kinds/`, `cel.nix` and `refuse.nix`. |

The shared functions in `lib/` are these:

- `kinds/`: the JSON Schema converter, the chalkcfg commands of `get`, the option types both
  recognise, and the API groups of chalkos's own kinds. It also holds the check of the kinds a
  grant, an output, a validator or an effect names, which lists every kind the image does not
  declare, Secret and withheld kinds.
- `cel.nix`: CEL that effect rules share.
- `refuse.nix`: `refuse`, which throws every problem a module finds in one message, and the
  phrases the messages share.

Each part of the router has a directory of `modules/node/router`:

- `kinds.nix` is a function of `{ chalkosLib, lib, routerLib }` that returns the part's kinds.
  `default.nix` loads it with `routerLib.kinds ./kinds.nix`.
- `default.nix` sets the kinds under one condition with the part's outputs, effects and units.
- A part whose diagnostics have a file of their own keeps them in `diagnostics.nix`, a module that
  `default.nix` imports, under the same condition.
- A feature that an image can leave out declares its option `chalkos.router.<feature>.enable` in
  `default.nix`.
- An output that reads another feature's kinds filters them by the kinds the image declares.

Every router image carries `firewall/`, `vrf/` and `policy-routing/`. An image can leave out
`bgp/`, `nat64/`, `resolver/`, `dhcp/`, `dhcp-relay/` and `wireguard/` with
`chalkos.router.<feature>.enable`.

## What a module gets

Every module of an image gets these arguments in addition to NixOS's own:

- `lib`: nixpkgs's library, unchanged.
- `chalkosLib`: chalkos's library, which holds the
  [extension API](../reference/extension-api.md#chalkoslib) and `chalkosLib.internal`.
  `lib/images.nix` passes it as a special argument, so `imports` and option declarations can use
  it. Submodules do not receive it. Code inside a module, submodules included, uses the module's
  argument, the same way it uses `pkgs` and `config`. mkImage refuses a `specialArgs.chalkosLib`
  of its caller.
- `pkgs`, with `pkgs.chalkos`: chalkos's programs, its Go toolchain `pkgs.chalkos.go`, the Go
  program builder `pkgs.chalkos.buildGoProgram`, `pkgs.chalkos.treeGoPaths`, the source paths
  of the whole Go tree, and `pkgs.chalkos.trimPackage`, which copies the programs and libraries
  of a package that an image runs, without its shell scripts and without a reference to the
  original. A module that leaves part of a package out of the image uses it.
- `routerLib`: what the router layer's parts have in common. `routerLib.types` holds the option
  types and CEL of the router kinds, and `routerLib.kinds` loads a part's kinds file.
  `modules/node/router/default.nix` sets it on every image, but it is internal to the router
  layer and not part of the extension API.
- The `specialArgs` given to mkImage or mkImages.

The flake exports the overlay that adds `pkgs.chalkos` as `overlays.default`. mkImage adds it
to an image's nixpkgs with `lib.mkBefore`, so the image's own overlays can override chalkos's
packages with `pkgs.chalkos.overrideScope`.

## Where a new helper goes

1. If it needs only `lib` and belongs to no layer, add it to chalkosLib in `lib/`, under
   `internal` unless extensions should call it.
2. If it builds a derivation that the flake or an image ships, it is a file in `nix/packages/`,
   named as the package. The overlay adds every file there to the scope, so a new package needs
   no other change. A package file takes its dependencies by name, as `callPackage` passes them.
   A file that takes an argument with the same name as a member of the scope gets that argument
   passed explicitly in `nix/overlay/default.nix`, as `kea.nix` gets nixpkgs's `kea`. `frr.nix`
   gets the `frr` from before the overlay, because images with FRR replace `pkgs.frr` with it.
3. If it needs a module's `config` or belongs to one layer, add it to that layer's module
   argument, as `routerLib` is for the router layer.
4. If one module uses it, put it in a file next to that module. A derivation that a module builds
   from its config is a file the module calls with `pkgs.callPackage`, such as
   `modules/node/kubernetes/kubelet.nix`.

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
- A feature that an image can leave out sets its config under `lib.mkIf` of its `enable`
  option, so a disabled feature leaves nothing in the image.
- chalkosLib holds no store paths. Anything that needs `pkgs` is a package in the scope.
- An evaluation test in `test/nix/` covers option behaviour, and a unit check covers a script. A VM
  test is for behaviour nothing smaller can show; [Testing](testing.md) explains the levels.

## Format and lint

`nix fmt` formats the Nix files. The `nix-lint` check fails on files nixfmt would change, on
unused bindings and arguments that deadnix finds, and on statix findings. `statix.toml` turns
off the `repeated_keys` lint and gives the reason.
[Development setup](development.md#format-and-lint) shows how to run the tools by hand.
