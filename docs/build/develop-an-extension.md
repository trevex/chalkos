---
title: "Develop and debug an extension"
description: "Check an extension from the fastest build to a node, find the cause of an evaluation error and fix common errors"
---

# Develop and debug an extension

This page is for an image author who changes an extension and wants to know soon whether it
works. It runs the checks from the fastest to the slowest: the kinds' schemas, the tests, the
image build, `chalkctl validate`, a dry run on a node and the upgrade. Each step catches mistakes
the steps before it cannot. The examples use the image `node-metal` of the
[extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions).

In short:

```sh
git add .
nix build .#nixosConfigurations.node-metal.config.system.build.chalkosSchemas
nix build -L .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults
nix flake check
nix build .#node-metal -o images/node-metal
chalkctl validate
chalkctl config apply server1 --dry-run --secrets=secrets.age
chalkctl upgrade --secrets=secrets.age
```

## Before you begin

You need an image flake with an extension, as [Write your first extension](first-extension.md)
builds one. The flake exports the images' evaluated systems and their tests, as the extensions
example's `flake.nix` does:

```nix
{
  nixosConfigurations = chalkos.lib.nixosOf images;
  checks.x86_64-linux = chalkos.lib.testsOf images;
}
```

The last three steps need the inventory and chalkctl. The last two also need a node and the
secrets file.

## Add new files to git

Nix reads a flake from the files git tracks. A new module, template or test file that git does not
track is missing from every build, which fails with
`error: Path '…' in the repository "…" is not tracked by Git.` and names the `git add` that fixes
it. Add each new file before you build, here those of the extensions example:

```sh
git add hardware.nix hardware.env.tmpl tests
```

The file need not be committed. Nix reads changes to tracked files from the working tree.

## Build the kinds' schemas

The schemas build in seconds, and chalkctl checks documents against them:

```console
$ nix build .#nixosConfigurations.node-metal.config.system.build.chalkosSchemas
$ ls result
chalkos.dev_v1alpha1_Host.json
chalkos.dev_v1alpha1_Secret.json
hardware.example.org_v1alpha1_Hardware.json
networkd.chalkos.dev_v1alpha1_Link.json
networkd.chalkos.dev_v1alpha1_NetDev.json
networkd.chalkos.dev_v1alpha1_Network.json
oci.chalkos.dev_v1alpha1_Mirror.json
oci.chalkos.dev_v1alpha1_Registry.json
rack.example.org_v1alpha1_Rack.json
storage.chalkos.dev_v1alpha1_SystemDisk.json
storage.chalkos.dev_v1alpha1_Volume.json
time.chalkos.dev_v1alpha1_TimeSync.json
```

This step catches a field whose option type has no JSON Schema.
[Look at the kind's JSON Schema](declare-a-kind.md#look-at-the-kinds-json-schema) reads a schema
with `jq`.

## Build the test results

The test results run every test of the image and keep what each test got. The build succeeds
whatever the tests got:

```console
$ nix build -L .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults
$ cat result/summary.txt
PASS chrony-empty
PASS chrony-servers
PASS hardware-server
PASS networkd-none
PASS networkd-static
PASS networkd-vlan
```

`-L` prints the standard error of renderers and checks as they run. `result/<test>/` holds what a
test rendered, refused and planned, so a new golden file comes from here. This step evaluates the
image's system, so it catches most evaluation errors, and it fails on a rule that does not
compile. [Test an extension](test-an-extension.md) declares the tests.

## Run nix flake check

`nix flake check` runs the tests and compares each with what it expects:

```sh
nix flake check
```

It fails on a test that fails, with a diff for each output whose files differ from the golden
files. [A failing test shows what it got](test-an-extension.md#a-failing-test-shows-what-it-got)
reads the failure.

## Build the image

The image build runs the checks that need the whole image, such as whether every effect acts on a
unit the image defines:

```sh
nix build .#node-metal -o images/node-metal
```

It takes longer than the steps before.
[The image build checks the image](test-and-roll-out.md#the-image-build-checks-the-image) lists
what it refuses.

## Validate the nodes' config

`chalkctl validate` checks each node's files against the new image's schemas and runs the rules
that read no facts:

```sh
chalkctl validate
```

It reaches no node. A rule that fails on a real document shows here first, such as one that reads
a misspelled field.

## Check a delivery on a node

`--dry-run` has the node run every check of a delivery, its renderers, validators and rules with
facts included, and changes nothing:

```sh
chalkctl config apply server1 --dry-run --secrets=secrets.age
```

A dry run checks the files against the image the node runs. To check them against a new image,
stage it with `chalkctl upgrade --until stage` and add `--staged` to the dry run.
[Check the change on the node](../operate/change-config.md#check-the-change-on-the-node) reads its
output.

## Roll the image out

Upgrade one node, check it, then upgrade the rest:

```sh
chalkctl upgrade server1 --secrets=secrets.age
chalkctl upgrade --secrets=secrets.age
```

[Test and roll out an image](test-and-roll-out.md) covers the upgrade and how to confirm the
outputs and services on the node.

## Find the cause of an evaluation error

An evaluation error names the option it concerns, such as
`systemd.services.hardware-info.chalkos.kinds`. Where it does not, add `--show-trace` to the build
that failed:

```sh
nix build --show-trace .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults
```

The trace prints every step of the evaluation. Its lines `while evaluating the option` and
`while evaluating definitions from` name the options and the files that led to the error.

`nix eval` prints one value of the image's configuration, such as the directory of an output or
what a unit reads:

```console
$ nix eval .#nixosConfigurations.node-metal.config.chalkos.outputs.hardware.dir
"/run/chalkos/outputs/hardware"
$ nix eval .#nixosConfigurations.node-metal.config.systemd.services.hardware-info.serviceConfig.EnvironmentFile
"-/run/chalkos/outputs/hardware/hardware.env"
$ nix eval --json .#nixosConfigurations.node-metal.config.chalkos.outputs --apply builtins.attrNames
["chrony","hardware","networkd"]
$ nix eval --json .#nixosConfigurations.node-metal.config.chalkos.effects.hardware-info
{"chalkd":null,"hint":null,"kinds":[],"outputs":["hardware"],"reload":[],"restart":["hardware-info.service"],"run":null,"start":false,"when":null}
```

`nix repl .` loads the flake's outputs, so you can explore the configuration by name:

```console
$ nix repl .
...
Added 3 variables.
checks, nixosConfigurations, packages
nix-repl> nixosConfigurations.node-metal.config.chalkos.outputs.hardware.dir
"/run/chalkos/outputs/hardware"
nix-repl> :p nixosConfigurations.node-metal.config.chalkos.tests.hardware-server
{
  before = null;
  config = /nix/store/…-source/hardware-examples/server.yaml;
  effects = null;
  facts = { };
  node = "example";
  oldFacts = «repeated»;
  outputs = { hardware = /nix/store/…-source/tests/server/hardware; };
  refusals = [ ];
}
```

## Common errors

Each message below comes from a build of the extensions example with one mistake added. "Shows
in" names the first step of the loop that shows it.

| Message | Shows in | Cause | Fix |
| --- | --- | --- | --- |
| `Path '…' in the repository "…" is not tracked by Git.` | every Nix command | A new file is not in git, so the flake lacks it. | `git add` the file. |
| `chalkos.kinds.Hardware.spec.biosFile: the option type absolute path has no JSON Schema mapping. Kinds accept str, …` | the schemas | A field's option type has no JSON Schema, here `path`. | Use a type of the field types reference, such as `nonEmptyStr`. |
| `attribute 'hardwre' missing` … `Did you mean hardware?` | the test results | `config.chalkos.outputs.hardwre.dir` names an output the image does not declare. | Use the output's name, as the error suggests. |
| `chalkos-rules: Hardware spec: rule "self.vendor = 'Supermicro'": ERROR: <input>:1:13: Syntax error: …` | the test results | The rule is no valid CEL. CEL compares with `==`. | Fix the expression the error points at. |
| `chalkos-rules: Hardware spec: rule "self.modle != ''": ERROR: <input>:1:5: undefined field 'modle'` | the test results | The rule reads a field the kind's schema does not declare. | Use the field's name as the kind declares it, here `model`. |
| `cannot check: Hardware/hardware: spec: the rule "self.serial != oldSelf.serial" cannot be evaluated: no such key: serial, a field the config leaves out; test it with has() first` | the test results, in `refusals differ`, when a test holds a document of the kind; `chalkctl validate` otherwise | The rule reads a field the schema declares but the document leaves out, such as one the running config's document lacks because its image did not declare the field yet. | Test the field with `has()` before reading it. |
| `systemd.services.hardware-info.chalkos.kinds: the image declares no kind Hardwre; it declares Hardware, …` | the test results | A unit's grant names a kind the image does not declare. | Fix the name, or add the module that declares the kind to the image. |
| `chalkos.outputs.server.inputs: the image declares no kind Server; it declares …` | the test results | An output's `inputs` names a kind the image does not declare. The `kinds` of an effect give the same error. | Fix the name, or add the module that declares the kind. |
| `chalkos.kinds.Rack.get: the path "spec.locaton" does not follow the kind's options: spec has no field locaton; its fields are location` | the test results | A `get` names a field the kind does not declare. | Use one of the fields the error lists. |
| `chalkos-rules: …/diagnostics.json: diagnostic pci-devices: its command or network namespace names {slot}, which is no parameter` | the test results | A placeholder in the diagnostic's command has no parameter. `nix flake check` and the image build report it under `Failed assertions:` as `chalkos.diagnostics.pci-devices: the command or networkNamespace names {slot}, which is no parameter`. | Declare `parameters.slot`, or drop the placeholder. |
| `chalkos-rules: effect hardware-log: the image defines no unit hardware-log.service` | the image build | An effect restarts a unit the image does not define. | Fix the unit's name, or define the unit. |

## What next

- [Test an extension](test-an-extension.md) declares the tests the loop runs.
- [Test and roll out an image](test-and-roll-out.md) covers the image build's checks and the
  rollout.
- [Change a kind incompatibly](change-a-kind.md) moves the nodes' documents to a new version of a
  kind.
- [Extension runtime](../reference/extension-runtime.md) lists the CEL environment, the template
  functions and the program contract.
