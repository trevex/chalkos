---
title: "Test an extension"
description: "Check what an image renders, refuses and reloads for a config, with tests that nix flake check runs"
---

# Test an extension

This page is for an image author whose extension renders files, refuses configs or reloads
services. At the end the extension carries tests: config files a node could be given, each with
the files its outputs render, the refusals it gets or the effects a change to it takes.
`nix flake check` runs them before any node gets the image. The page uses the tests of the
homelab's BGP extension, `extensions/bgp.nix`.

A test runs a config through the checks chalkd runs for a delivery: the kinds' schemas, the change
rules, the validators, every output's renderer and check, and the plan of effects. It runs them in
the Nix sandbox, without a node, and compares what it got with what the test expects.

## Before you begin

You need an image flake made with `mkImages`, as [Your first image flake](first-image.md) makes
it, and an extension with an output, a validator or an effect, as
[Render config files](render-config.md), [Validate config with a program](validate-config.md) and
[React to changes](react-to-changes.md) declare them.

## A test is a config and what the image makes of it

`chalkos.tests.<name>` declares a test. `extensions/bgp.nix` declares these two in
`chalkos.tests`:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:tests"
```

`config` is a YAML or JSON file of a node's documents, as a delivery gives them. The test checks
them as a set, without the documents of the node's identity, so a test's file holds no
`SystemDisk` and only the documents it is about. `bgp-examples/fabric.yaml` holds a node's
`Speaker`, the `Peer` of the router, the `Secret` with its password and the `Network` documents
the validator `bgp-peers` reads.

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
same, and only its files go uncompared. The [extension API](../reference/extension-api.md#chalkostests)
lists each field.

Besides the extension's tests, an image carries the tests of chalkos's own outputs, such as
`chrony-servers` and `networkd-vlan`, which render the examples of those outputs.

## Golden files hold what an output renders

`outputs.bird-conf = ./tests/fabric/bird-conf` names a directory that holds exactly the files
`bird-conf` renders from the config. A missing, extra or different file fails the test.

The golden files are what the test renders, once checked by eye. Declare the test without
`outputs`, and build what each test got:

```sh
nix build .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults
```

The build succeeds whatever the tests got. `result` holds a directory per test, with every output
the test rendered:

```console
$ cat result/summary.txt
PASS bgp-configured-interfaces
PASS bgp-fabric
...
PASS networkd-vlan
$ ls result/bgp-fabric result/bgp-fabric/outputs
result/bgp-fabric:
effects.json  outputs  refusals.json

result/bgp-fabric/outputs:
bird-conf  chrony  networkd
```

Read `result/bgp-fabric/outputs/bird-conf/bird.conf`. When it is the file bird should get, copy
the directory into the flake, add it to git, since a flake sees only the files git tracks, and
name it in the test:

```sh
mkdir -p extensions/tests/fabric
cp -r --no-preserve=mode result/bgp-fabric/outputs/bird-conf extensions/tests/fabric/bird-conf
git add extensions/tests/fabric
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

A test of a config the image refuses lists the refusals it expects. The homelab's
`bgp-unconfigured-peer-interface` gives the `Peer` a third interface, `enp3s0`, without a `Network`
document, which [Validate config with a program](validate-config.md) shows the validator refuse:

```nix
chalkos.tests.bgp-unconfigured-peer-interface = {
  config = ./tests/unconfigured-peer-interface.yaml;
  refusals = [
    {
      kind = "Peer";
      name = "router";
      path = "spec.interfaces[2]";
      message = "enp3s0 has no Network document; bird binds the session to an interface the node configures";
    }
  ];
};
```

`bgp.nix` declares its refusal tests through a function of its own, `validatorTest`, which takes
the name of the file below `tests/` and the refusals. Each refusal has the fields of a validator's
report: `kind` and `name` of the document, `path` of the field, and `message` exactly as chalkctl
prints it. The test compares the whole list, in the order chalkctl prints the refusals, each once.
`result/<test>/refusals.json` holds what the test got:

```console
$ cat result/bgp-unconfigured-peer-interface/refusals.json
[{"kind":"Peer","name":"router","path":"spec.interfaces[2]","message":"enp3s0 has no Network document; bird binds the session to an interface the node configures"}]
```

Refusals come from every stage of the check. A change rule's refusal needs `before`, since the
rule compares with the config the node runs. The homelab's `bgp-new-asn` has
`before = ./bgp-examples/fabric.yaml` and a config whose `Peer` has the AS number 65002, which
the `Peer` rule of [Guard changes with rules and facts](guard-changes.md) refuses:

```console
$ cat result/bgp-new-asn/refusals.json
[{"kind":"Peer","name":"router","path":"spec.asn","message":"the AS number stays 65000; add a Peer of another name for 65002"}]
```

A schema's refusal, and any other failure of the check that names no field, has an empty `kind`,
`name` and `path`. A `Peer` with `asn: 0` gets this one:

```json
[{"kind":"","name":"","path":"","message":"Peer \"router\": - at '/spec/asn': minimum: got 0, want 1"}]
```

A test of such a refusal leaves those three fields out, since each defaults to the empty string,
and gives the `message` alone.

## Effects are those of a change

A test with `before` checks a change from the documents of `before` to those of `config`. The
change rules compare the two, and the effects due are those of the change. `bgp-new-password`
changes the password in the `Peer`'s `Secret` and nothing else. `bird.conf` names the credential
and not its value, so the file stays the same, and the golden files are those of `bgp-fabric`. The
effect `bird` of [React to changes](react-to-changes.md) still fires, because its `outputs` trigger
counts a changed value of a `Secret` that the input documents of `bird-conf` reference:

```console
$ cat result/bgp-new-password/effects.json
["bird"]
```

`effects` lists the names of the effects of `chalkos.effects` the change takes, sorted by name, as
chalkd's plan lists them, and `[ ]` expects none. A test that sets `effects` needs `before`, and
`before` must be accepted, or the test fails. Without `before` the evaluation fails:

```text
error: chalkos.tests.bgp-new-password.effects: effects are those of a change, so the test needs before
```

An effect whose `when` reads a fact gets that fact from the test's `facts`, and from `oldFacts` for
the state before the change. A fact the test leaves out reads as null, and a fact the image does
not declare fails the evaluation.

## nix flake check runs the tests

`mkImages` returns each image's tests as `checks.<variant>-<platform>`, and the homelab's flake
exports them as its checks:

```nix
checks.${system} = images.checks;
```

`nix flake check` then builds every image's tests, along with the flake's other checks:

```console
$ nix flake check
...
checking derivation checks.x86_64-linux.router-metal...
checking derivation checks.x86_64-linux.node-metal...
...
running 3 flake checks...
all checks passed!
```

To run one image's tests, build its check:

```sh
nix build .#checks.x86_64-linux.node-metal
```

The image's build runs no tests by default, and the tests change neither the image's content nor
its root hash. To make the build of an image fail when one of its tests fails, set
`chalkos.testsOnBuild` in its variant's modules:

```nix
chalkos.testsOnBuild = true;
```

## A failing test shows what it got

A failing test fails its check with a `FAIL` line, and with a diff for each output whose files
differ from its golden files. Say `bird.conf.tmpl` changes `connect retry time 5` to `10`.
`nix flake check` fails on `checks.x86_64-linux.node-metal` and shows the end of the log.
`nix log` prints all of it:

```console
$ nix log /nix/store/sssdj3ip9jl1c68dhamqkv8lx872lhf7-chalkos-tests.drv
FAIL bgp-fabric: output bird-conf differs from its golden files
FAIL bgp-new-password: output bird-conf differs from its golden files

test bgp-fabric, output bird-conf: the golden files differ from what it rendered:
diff -ruN /nix/store/bmiyqqwiamjm9y4x1rj849z6as1qk70b-bird-conf/bird.conf /nix/store/0h116raxpzn4ih4i6m4mg7rsx2jrrhm4-chalkos-test-results/bgp-fabric/outputs/bird-conf/bird.conf
--- /nix/store/bmiyqqwiamjm9y4x1rj849z6as1qk70b-bird-conf/bird.conf	1970-01-01 00:00:01.000000000 +0000
+++ /nix/store/0h116raxpzn4ih4i6m4mg7rsx2jrrhm4-chalkos-test-results/bgp-fabric/outputs/bird-conf/bird.conf	1970-01-01 00:00:01.000000000 +0000
@@ -27,7 +27,7 @@
   neighbor fdc8:a1b2:c3d4:1::1 as 65000;
   interface "enp1s0";
   direct;
-  connect retry time 5;
+  connect retry time 10;
   bfd on;
   authentication md5;
   password 'secret_bgp.password';
...
to take what it rendered, with DIR the directory of chalkos.tests.bgp-fabric.outputs.bird-conf:
  rm -r DIR && cp -r --no-preserve=mode /nix/store/0h116raxpzn4ih4i6m4mg7rsx2jrrhm4-chalkos-test-results/bgp-fabric/outputs/bird-conf DIR && git add DIR
...
```

When the new file is the one bird should get, run the command that follows its diff, with `DIR`
the golden directory, here `extensions/tests/fabric/bird-conf`. Both tests name that
directory, so one copy mends both.

A refusal or an effect that differs gets one line, with what the test expected and what it got:

```text
FAIL bgp-asn-zero: refusals differ: expected [], got [{"kind":"","name":"","path":"","message":"Peer \"router\": - at '/spec/asn': minimum: got 0, want 1"}]
FAIL bgp-new-password: effects differ: expected [], got ["bird"]
```

An effect's `when` that cannot be evaluated fails no test. The summary and the log show it as a
`WARN` line, and `result/<test>/warnings` holds it.

## Check that it worked

Run the checks of the flake:

```sh
nix flake check
```

It ends with `all checks passed!`. `result/summary.txt` of `chalkosTestResults` lists a `PASS`
line for each test, the extension's among them.

## What next

- [Test and roll out an image](test-and-roll-out.md) takes the tested image to the nodes.
- [Validate config with a program](validate-config.md) for the refusals a validator gives.
- [React to changes](react-to-changes.md) for the effects a change takes.
- The [extension API](../reference/extension-api.md#chalkostests) for every field of a test.
