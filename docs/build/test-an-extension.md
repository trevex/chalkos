---
title: "Test an extension"
description: "Check what an image renders, refuses and reloads for a config, with tests that nix flake check runs"
---

# Test an extension

This page is for an image author whose extension renders files, refuses configs or reloads
services. At the end the extension carries tests: config files a node could be given, each with
the files its outputs render, the refusals it gets or the effects a change to it takes.
`nix flake check` runs them before any node gets the image. The examples use the tests of the
[extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions) and its
`Hardware` and `Rack` kinds.

A test runs a config through the checks chalkd runs for a delivery: the kinds' schemas, the change
rules, the validators, every output's renderer and check, and the plan of effects. It runs them in
the Nix sandbox, without a node, and compares what it got with what the test expects.

See it in a full extension:
[Keep the rendered file as golden files](../guides/bgp-extension.md#keep-the-rendered-file-as-golden-files)
of the BGP guide tests bird's config, an effect, a change rule and a validator.

## Before you begin

You need an image flake made with `mkImages`, as [Your first image flake](first-image.md) makes
it, and an extension with an output, a validator or an effect, as
[Render config files](render-config.md), [Validate config with a program](validate-config.md) and
[React to changes](react-to-changes.md) declare them.

## A test is a config and what the image makes of it

`chalkos.tests.<name>` declares a test. `hardware.nix` declares this one:

```nix title="hardware.nix"
--8<-- "examples/extensions/hardware.nix:test"
```

`config` is a YAML or JSON file of a node's documents, as a delivery gives them. The test checks
them as a set, without the documents of the node's identity, so a test's file need not hold a
`SystemDisk`, and holds only the documents it is about. `hardware-examples/server.yaml` holds one
`Hardware` document:

```yaml title="hardware-examples/server.yaml"
--8<-- "examples/extensions/hardware-examples/server.yaml"
```

The other fields say what the test expects:

| Field | Default | What it holds |
| --- | --- | --- |
| `outputs.<output>` | `{ }` | A directory of the files the output renders, its golden files. |
| `refusals` | `[ ]` | The refusals the config gets. The default expects it accepted. |
| `before` | `null` | The documents before `config`, which make the test one of a change. |
| `effects` | `null` | The names of the effects the change takes. `null` leaves them unchecked. |
| `node` | `example` | The node's name, which rules read as `node.name` and programs as `.node.name`. |
| `facts`, `oldFacts` | `{ }` | The facts that rules and effects' `when` read, by name, after and before the change. |

The refusals are always compared, so every test also checks that its config is accepted or
refused as expected. An output the test leaves out of `outputs` is rendered and checked all the
same, only its files are not compared. The
[extension API](../reference/extension-api.md#chalkostests) lists each field.

Besides the extension's tests, an image carries chalkos's tests of its `chrony` and `networkd`
outputs, one per config file of `modules/node/examples`, such as `chrony-servers` and
`networkd-vlan`.

## Golden files hold what an output renders

`outputs.hardware = ./tests/server/hardware` names a directory that holds exactly the files
`hardware` renders from the config. A missing, extra or different file fails the test.

The golden files are what the test renders, once checked by eye. Declare the test without
`outputs`, and build what each test got:

```sh
nix build .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults
```

The build succeeds whatever the tests got. `result` holds a directory per test, with every output
the test rendered:

```console
$ cat result/summary.txt
PASS chrony-empty
PASS chrony-servers
PASS hardware-server
PASS networkd-none
PASS networkd-static
PASS networkd-vlan
$ ls result/hardware-server result/hardware-server/outputs
result/hardware-server:
effects.json  outputs  refusals.json

result/hardware-server/outputs:
chrony  hardware  networkd
```

Read `result/hardware-server/outputs/hardware/hardware.env`. When it is the file the unit should
get, copy the directory into the flake, add it to git, since a flake sees only the files git
tracks, and name it in the test:

```sh
mkdir -p tests/server
cp -r --no-preserve=mode result/hardware-server/outputs/hardware tests/server/hardware
git add tests/server
```

A store path in a rendered file changes whenever a dependency of the image changes, so the test
writes the hash of each store path as `eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee`, in the results and in the
golden files alike. A router image's `unbound.conf` holds this line:

```text
  trust-anchor-file: "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-dns-root-data-2025-04-14/root.key"
```

An output that renders no files has no golden directory in a git repository, because git keeps no
empty directory, so a test leaves such an output out of `outputs`.

## Refusals name the document, the field and the message

A test of a config the image refuses lists the refusals it expects. The `Rack` rule of
[Guard changes with rules and facts](guard-changes.md#read-node-state-from-a-fact) reads the fact
`rackSurvey`, which a test gives in `facts`. This test gives a survey that found the node
elsewhere than its `Rack` says:

```nix
chalkos.tests.rack-moved = {
  config = ./tests/rack.yaml;
  facts.rackSurvey = {
    location = "rack-b/u3";
  };
  refusals = [
    {
      kind = "Rack";
      name = "rack";
      path = "spec.location";
      message = "the survey found the node at rack-b/u3, not at rack-a/u12; fix the location, or survey the node again";
    }
  ];
};
```

Each refusal has the fields of a validator's report: `kind` and `name` of the document, `path` of
the field, and `message` exactly as chalkctl prints it. The test compares the whole list, in the
order chalkctl prints the refusals, each once. `result/<test>/refusals.json` holds what the test
got:

```console
$ cat result/rack-moved/refusals.json
[{"kind":"Rack","name":"rack","path":"spec.location","message":"the survey found the node at rack-b/u3, not at rack-a/u12; fix the location, or survey the node again"}]
```

Refusals come from every stage of the check. A change rule that reads `oldSelf` refuses only in a
test with `before`, since the rule compares with the config the node runs.

A schema's refusal, and any other failure of the check that names no field, has an empty `kind`,
`name` and `path`. A `Hardware` with `model` misspelled as `modle` gets this one:

```json
[{"kind":"","name":"","path":"","message":"Hardware \"hardware\": - at '/spec': additional properties 'modle' not allowed\n- at '/spec': missing property 'model'"}]
```

A test of such a refusal leaves those three fields out, since each defaults to the empty string,
and gives the `message` alone.

## Effects are those of a change

A test with `before` checks a change from the documents of `before` to those of `config`. The
change rules compare the two, and the effects due are those of the change. Here `config` gives the
server of `hardware-examples/server.yaml` the asset tag `HL-0043`. `hardware.env` changes, so the
effect `hardware-info` of [React to changes](react-to-changes.md) fires:

```nix
chalkos.tests.hardware-new-asset-tag = {
  before = ./hardware-examples/server.yaml;
  config = ./tests/new-asset-tag.yaml;
  effects = [ "hardware-info" ];
};
```

```console
$ cat result/hardware-new-asset-tag/effects.json
["hardware-info"]
```

`effects` lists the names of the effects of `chalkos.effects` the change takes, sorted by name, as
chalkd's plan lists them, and `[ ]` expects none. A test that sets `effects` needs `before`.
Without it the evaluation fails:

```text
error: chalkos.tests.hardware-new-asset-tag.effects: effects are those of a change, so the test needs before
```

A `before` that the image refuses fails the test.

An effect whose `when` reads a fact gets that fact from the test's `facts`, and from `oldFacts` for
the state before the change. A fact the test leaves out reads as null, and a fact the image does
not declare fails the evaluation.

## nix flake check runs the tests

Each image `mkImages` returns carries its tests as `tests`. `chalkos.lib.testsOf` collects them by
the images' names, and the extensions example's flake exports them as its checks:

```nix
checks.x86_64-linux = chalkos.lib.testsOf images;
```

`nix flake check` then builds every image's tests, along with the flake's other checks:

```console
$ nix flake check
evaluating flake...
checking flake output 'packages'...
checking derivation packages.x86_64-linux.node-metal...
derivation evaluated to /nix/store/074srcqyv18ccx6hx2bi3i509i90f9xb-chalkos-image.drv
checking flake output 'nixosConfigurations'...
checking NixOS configuration 'nixosConfigurations.node-metal'...
checking flake output 'checks'...
checking derivation checks.x86_64-linux.node-metal...
derivation evaluated to /nix/store/gzf2c16shp7qavf32rj28b9rryh8p8w6-chalkos-tests.drv
running 2 flake checks...
building '/nix/store/s3qr7ib2r3hh32bs9vvhpk3v6y7g4bpq-chalkos-test-results.drv'...
building '/nix/store/gzf2c16shp7qavf32rj28b9rryh8p8w6-chalkos-tests.drv'...
all checks passed!
```

To run one image's tests, build its check:

```sh
nix build .#checks.x86_64-linux.node-metal
```

The image's build runs no tests by default, and the tests change neither the image's content nor
its root hash. To make the build of an image fail when one of its tests fails, set
`chalkos.testsOnBuild` in its modules:

```nix
chalkos.testsOnBuild = true;
```

## A failing test shows what it got

A failing test fails its check with a `FAIL` line, and with a diff for each output whose files
differ from its golden files. Say `hardware.env.tmpl` renames `MODEL` to `SERVER_MODEL`.
`nix flake check` fails on `checks.x86_64-linux.node-metal` and shows the end of the log.
`nix log` prints all of it:

```console
$ nix log /nix/store/6yvb277n46dvsrb3klmzlhfrgymj6mj7-chalkos-tests.drv
FAIL hardware-server: output hardware differs from its golden files

test hardware-server, output hardware: the golden files differ from what it rendered:
diff -ruN /nix/store/vkkqfy6rabgxblk9gsbwbp3ban80mizz-hardware/hardware.env /nix/store/l9mvgkpb0q7xiwg0cgwzl9yxa13m01pa-chalkos-test-results/hardware-server/outputs/hardware/hardware.env
--- /nix/store/vkkqfy6rabgxblk9gsbwbp3ban80mizz-hardware/hardware.env	1970-01-01 00:00:01.000000000 +0000
+++ /nix/store/l9mvgkpb0q7xiwg0cgwzl9yxa13m01pa-chalkos-test-results/hardware-server/outputs/hardware/hardware.env	1970-01-01 00:00:01.000000000 +0000
@@ -1,3 +1,3 @@
 VENDOR="Supermicro"
-MODEL="SYS-E300-9D"
+SERVER_MODEL="SYS-E300-9D"
 ASSET_TAG="HL-0042"
to take what it rendered, with DIR the directory of chalkos.tests.hardware-server.outputs.hardware:
  rm -r DIR && cp -r --no-preserve=mode /nix/store/l9mvgkpb0q7xiwg0cgwzl9yxa13m01pa-chalkos-test-results/hardware-server/outputs/hardware DIR && git add DIR
```

When the new file is the one the unit should get, run the command that follows its diff, with
`DIR` the golden directory, here `tests/server/hardware`. Each test that names the directory then
compares with the new file.

Say a test `hardware-no-model` gives the `Hardware` the misspelled `modle` and expects the config
accepted, and `hardware-new-asset-tag` sets `effects = [ ]`. Each gets one line, with what the
test expected and what it got:

```text
FAIL hardware-new-asset-tag: effects differ: expected [], got ["hardware-info"]
FAIL hardware-no-model: refusals differ: expected [], got [{"kind":"","name":"","path":"","message":"Hardware \"hardware\": - at '/spec': additional properties 'modle' not allowed\n- at '/spec': missing property 'model'"}]
```

An effect's `when` that cannot be evaluated fails no test. The summary and the log show it as a
`WARN` line, and `result/<test>/warnings` holds it.

## Check that it worked

Run the checks of the flake:

```sh
nix flake check
```

It ends with `all checks passed!`. After
`nix build .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults`,
`result/summary.txt` lists a `PASS` line for each test, the extension's among them.

## What next

- [React to changes](react-to-changes.md) for the effects a change takes.
- [Validate config with a program](validate-config.md) for the refusals a validator gives.
- [Test and roll out an image](test-and-roll-out.md) takes the tested image to the nodes.
- The [extension API](../reference/extension-api.md#chalkostests) for every field of a test.
