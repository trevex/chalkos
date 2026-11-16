---
title: "Write a BGP extension"
description: "Build the homelab's bird extension from an empty module, test each part, and debug it on a node"
---

# Write a BGP extension

This tutorial builds the homelab's BGP extension, `extensions/bgp.nix`, from an empty module. The
extension runs bird on each node, with two config kinds of its own: `Speaker` for the node's side
of its sessions and `Peer` for its neighbours. At the end the nodes' image declares both kinds,
renders `bird.conf` from them, runs bird with the sessions' password, reloads it on a change,
refuses config bird cannot run, and shows bird's sessions to an operator. Each step adds one part,
shows how to look at what it produced, and tests it before the next. The last part debugs the
extension on a running node. Plan about an hour, most of it for builds.

The [Build images](../build/index.md) pages explain each part in depth. This page puts them in the
order of the work and links to them where the details matter.

## Before you begin

You need:

- the homelab example in a git repository of its own, with the flake's shell, as
  [Prepare the files](home-lab.md#prepare-the-files) and [Get the tools](home-lab.md#get-the-tools)
  of the home lab set it up. Run every command of this page in that shell, in the repository;
- some Nix: NixOS modules, options and `let` bindings;
- for the last part, a node that runs the image `node-metal`. [Build a home lab](home-lab.md)
  installs three. Without the machines, the [quick start](../get-started/quick-start.md) runs a
  lab of virtual machines, whose image takes the extension the same way.

The steps up to [Try it on a node](#try-it-on-a-node) need no node: they run in the Nix sandbox.

## Start from an empty module

The example already holds the finished extension. Move it aside, to compare with at any step, and
start an empty one:

```sh
git mv extensions extensions.done
mkdir extensions
```

`flake.nix` puts the directory into the nodes' image, and leaves it out of the router's:

```nix
{
  name = "node-metal";
  platform = "metal";
  modules = [
    chalkos.nixosModules.kubernetes
    ./extensions
  ];
}
```

`extensions/default.nix` imports the extension:

```nix title="extensions/default.nix"
# The homelab's extension for the nodes' image: BGP with bird, its Speaker and Peer kinds and the
# config rendered from them.
{
  imports = [ ./bgp.nix ];
}
```

`extensions/bgp.nix` starts with the module's arguments and two types the kinds use:

```nix title="extensions/bgp.nix"
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  # An interface's name as bird.conf quotes it: the characters of a Linux interface name, 15 at
  # most, without globs.
  interfaceName = lib.types.strMatching "[A-Za-z0-9_.-]{1,15}";
  # An octet of an IPv4 address in decimal, without leading zeros.
  octet = "25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9]";
in
{
}
```

`chalkosLib` is chalkos's library for image modules: the kinds' option types, the renderers and
the effects' helpers. Every later step adds attributes to the module's body. Add the new files to
git, because a flake sees only the files git tracks:

```sh
git add extensions
```

A file that git does not track is missing from the build, and the error names a path that does not
exist. Run `git add` after each new file of the steps below.

## Declare the kinds

A kind is the schema of a config document. The image converts its options into a JSON Schema that
chalkctl and chalkd check every document against. Add `Peer` to the module's body:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:kind"
```

Each field is a NixOS option. The type decides what the schema accepts, so pick the narrowest one:
every value here lands in `bird.conf`, and `chalkosLib.types.address` and `interfaceName` keep a
value from adding a statement of its own. A field without a default is required. `rules` and
`unique` need `chalkosLib.mkOption` in place of `lib.mkOption`.
[Declare a config kind](../build/declare-a-kind.md) lists the types and what each becomes.

Add `Speaker`, of which a node holds one, since it has no `multiple`:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:speaker"
```

### Look at the schemas

The schemas are a build output of their own, which takes seconds and needs no image. Build them
and print a field:

```console
$ nix build .#nixosConfigurations.node-metal.config.system.build.chalkosSchemas
$ jq .properties.spec.properties.asn result/bgp.example.org_v1alpha1_Peer.json
{
  "description": "AS number of the neighbour.",
  "examples": [
    65000
  ],
  "maximum": 4294967295,
  "minimum": 1,
  "type": "integer",
  "x-chalkos-rules": [
    {
      "description": "A Peer keeps its AS number.",
      "messageExpression": "\"the AS number stays \" + string(oldSelf) + \"; add a Peer of another name for \" + string(self)",
      "rule": "self == oldSelf",
      "takes": "refuse"
    }
  ]
}
```

`result/` holds a file per kind the image accepts. A type that has no JSON Schema, a rule that does
not compile or a misspelled option fails this build, with the option's path in the message. It is
the fastest check of a kind: run it after each change to one.

### Validate the nodes' files

The homelab's files hold `Speaker` and `Peer` documents: `bgp/fabric.yaml` the `Peer` and the
ports' `Network` documents, and each `nodes/node<n>.yaml` its node's `Speaker`. `chalkctl validate`
checks them against the schemas of the image the inventory names. It reads the built image, so
build it first:

```sh
nix build .#node-metal -o images/node-metal
chalkctl validate
```

The image build takes minutes, so keep it for the files of the nodes. With `asn` misspelled as
`asm` in `bgp/fabric.yaml`, `chalkctl validate` refuses all three nodes:

```console
$ chalkctl validate
chalkctl: the config of 3 nodes is invalid:
node node1: Peer "router": - at '/spec': additional properties 'asm' not allowed
- at '/spec': missing property 'asn'
node node2: Peer "router": - at '/spec': additional properties 'asm' not allowed
- at '/spec': missing property 'asn'
node node3: Peer "router": - at '/spec': additional properties 'asm' not allowed
- at '/spec': missing property 'asn'
```

## Render bird.conf

bird reads a config file of its own format, so an output renders it from the documents. Copy the
template from the finished extension:

```sh
cp extensions.done/bird.conf.tmpl extensions/
git add extensions
```

```text title="extensions/bird.conf.tmpl"
--8<-- "examples/homelab/extensions/bird.conf.tmpl"
```

The template is Go's `text/template`. It reads the node's name as `.node.name` and each input
kind's documents as a list, `.documents.Speaker` and `.documents.Peer`, sorted by name and with
the schemas' defaults filled in. `quote` and `refuse` are chalkos's own functions:
[Render config files](../build/render-config.md#a-template-renders-one-file) lists them.

The output's check runs bird's parser on the rendered file, so the module needs bird's package.
Add it to the `let` bindings:

```nix
bird = config.services.bird.package;
```

Then declare the output in the body:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:output"
```

On a node, chalkd renders and checks every output before a delivery changes anything, and refuses
the delivery when one fails. The same renderer and check run in the image's tests, without a node.

### See what the output renders

A test gives the image a config and records what the image makes of it. Copy the example config of
a homelab node, which holds a `Speaker`, the `Peer`, the `Secret` with the password and the
`Network` documents of the ports, and declare a test of it in the body:

```sh
mkdir extensions/bgp-examples
cp extensions.done/bgp-examples/fabric.yaml extensions/bgp-examples/
git add extensions
```

```nix
chalkos.tests.bgp-fabric.config = ./bgp-examples/fabric.yaml;
```

Build the results of the image's tests. The build takes seconds, needs no image, and succeeds
whatever the tests got:

```console
$ nix build .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults
$ cat result/summary.txt
PASS bgp-fabric
PASS chrony-empty
PASS chrony-servers
PASS networkd-none
PASS networkd-static
PASS networkd-vlan
```

The `chrony-*` and `networkd-*` tests are chalkos's own, which every image carries. `bgp-fabric`
passes because it expects nothing yet beyond an accepted config. Read what the output rendered:

```sh
cat result/bgp-fabric/outputs/bird-conf/bird.conf
```

It holds a `protocol bgp` per interface of the `Peer`, as
[Render config files](../build/render-config.md#a-template-renders-one-file) shows in full. After each
change to the template, build the results again and read the new file.

### Find out why a render fails

Say the template loses the `;` after `router id`. bird's parser refuses the file, and the test
gets the refusal a delivery would get:

```console
$ cat result/summary.txt
FAIL bgp-fabric: refusals differ: expected [], got [{"kind":"","name":"","path":"","message":"output bird-conf: its check refused the rendered files (exit 1)"}]
...
```

The refusal names the output and the exit status alone, as on a node, because a check's output can
echo the documents. What bird printed is in the build's log. Build with `-L` to print it:

```console
$ nix build -L .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults
...
chalkos-test-results> 2026/11/16 07:52:35 output bird-conf: its check /nix/store/39kg0jfg9wiagx56jrf3hpnwjvh6xv44-chalkos-output-bird-conf-check failed (exit 1): "bird: 2026-11-16 07:52:35.151 [0000] <FATAL> /build/chalkos-tests-2581098952/.staging/bird-conf.1551592570/bird.conf:4:1 syntax error, unexpected PROTOCOL, expecting ';'"
...
```

Line 4 of the rendered file starts with `protocol`, where bird expected the `;` of the line
before. A render that fails leaves no files in `result/<test>/outputs`, so the log is the place to
look.

### Keep the rendered file as golden files

Once `bird.conf` is the file bird should get, keep it in the repository, and have the test compare
every later render with it:

```sh
mkdir -p extensions/tests/fabric
cp -r --no-preserve=mode result/bgp-fabric/outputs/bird-conf extensions/tests/fabric/bird-conf
git add extensions
```

```nix
chalkos.tests.bgp-fabric = {
  config = ./bgp-examples/fabric.yaml;
  outputs.bird-conf = ./tests/fabric/bird-conf;
};
```

From now on a template change that alters the file fails `bgp-fabric` with a diff, and prints the
command that takes the new file, as
[A failing test shows what it got](../build/test-an-extension.md#a-failing-test-shows-what-it-got)
shows.

## Run bird

The image needs bird itself, reading the rendered file. Add the service to the body:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:service"
```

`bird.conf` exists on the node alone, so the NixOS module's check of the config is off, and the
output's check takes its place. The include of the output's `dir` names the output by its option,
so a misspelled name fails the evaluation. The service's settings run `bird-credentials`, which
turns the sessions' password into bird constants. Add it to the `let` bindings:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:credentials"
```

A renderer never sees a secret's value, so `bird.conf` names the password by its credential,
`'secret_bgp.password'`. A grant of the `Peer` kind has chalkd hand bird the values that the
`Peer` documents' `password` fields name, as systemd credentials:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:grant"
```

[Pass secrets to a service](../build/pass-secrets.md) follows the value from the `Secret` document
to bird. Last, two settings outside bird: networkd leaves the routes bird installs alone, and the
firewall lets BFD's packets in:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:network"
```

Build the image. The build checks what the evaluation cannot, such as a granted unit that runs
nothing:

```sh
nix build .#node-metal -o images/node-metal
```

## Reload bird on a change

A delivery that changes `bird.conf` must reach bird. An effect says what a delivery does to a
service once it is applied. Add it to the body:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:effect"
```

bird reloads when the files of `bird-conf` change, or when a value of a `Secret` that the output's
documents reference changes. Check the effect with a test of a change: `before` holds the config
the node runs, and `config` the one delivered. Copy the example's config with a new password, and
replace `chalkos.tests.bgp-fabric` with `chalkos.tests = { ... };` holding these two tests:

```sh
cp extensions.done/tests/fabric-new-password.yaml extensions/tests/
git add extensions
```

```nix
--8<-- "examples/homelab/extensions/bgp.nix:tests"
```

`bird.conf` names the credential, not its value, so the new password leaves the file as it was,
and the test compares it with the same golden files. The effect fires all the same, and the
results show it:

```console
$ cat result/bgp-new-password/effects.json
["bird"]
```

[React to changes](../build/react-to-changes.md) covers restarts, commands and conditions.

## Refuse what bird cannot run

A schema checks each document on its own. Two more checks refuse a config before it reaches bird:
a change rule, which compares a document with the one the node runs, and a validator, a program
that reads documents of several kinds.

### Keep a Peer's AS number

The `asn` field of the `Peer` you declared carries a rule, `self == oldSelf`: a neighbour with a new
AS number is a new neighbour, so the node refuses the change. Copy the example's config with a new
AS number, and test the refusal:

```sh
cp extensions.done/tests/new-asn.yaml extensions/tests/
git add extensions
```

```nix
chalkos.tests.bgp-new-asn = {
  before = ./bgp-examples/fabric.yaml;
  config = ./tests/new-asn.yaml;
  refusals = [
    {
      kind = "Peer";
      name = "router";
      path = "spec.asn";
      message = "the AS number stays 65000; add a Peer of another name for 65002";
    }
  ];
};
```

The test passes when the refusals are exactly these. `result/bgp-new-asn/refusals.json` holds what
the test got:

```console
$ cat result/bgp-new-asn/refusals.json
[{"kind":"Peer","name":"router","path":"spec.asn","message":"the AS number stays 65000; add a Peer of another name for 65002"}]
```

[Guard changes with rules and facts](../build/guard-changes.md) covers rules over several fields,
over all of a kind's documents, and rules that read the node's state.

### Refuse an interface the node does not configure

bird binds each session to an interface. A `Peer` on an interface that no `Network` document
configures gets no session, and no schema can tell, because the names sit in documents of another
kind. A validator reads the documents of several kinds and prints the refusals it finds. Copy the
example's jq program and declare it:

```sh
cp extensions.done/bgp-peers.jq extensions/
git add extensions
```

```nix
--8<-- "examples/homelab/extensions/bgp.nix:validator"
```

The program reads the same input as a renderer, and prints a report of refusals, one per
interface that no `Network` names. [Validate config with a program](../build/validate-config.md)
walks through it. Test it with a config whose `Peer` names a third interface, `enp3s0`, without a
`Network` document:

```sh
cp extensions.done/tests/unconfigured-peer-interface.yaml extensions/tests/
git add extensions
```

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

The finished extension tests the validator on more configs: interfaces matched by lists, globs and
names separated by spaces, a `Peer` without a `Speaker`, and a node without any `Peer`. It declares
them through a function of its own, `validatorTest`, that takes the name of the file below `tests/`
and the refusals. Cover each branch of a program this way: a config it accepts, and one per
refusal.

## Add a diagnostic

A node has no logins, so an operator reads bird's state through a diagnostic, a read-only command
of the image that chalkd runs on request. Add it to the body:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:diagnostic"
```

`birdc -r` asks bird in its read-only mode, through bird's socket, which `groups` makes reachable.
`protocol` takes only names of the form `bird.conf` gives the sessions, so no value closes bird's
quotes. The image's build checks the declaration: a placeholder without a parameter, or a group the
image lacks, fails it. [Add a diagnostic](../build/add-a-diagnostic.md) lists the parameter types
and the sandbox the command runs in.

## Run every test

The flake exports the images' tests as its checks. Run them, and compare the module with the
finished one:

```sh
nix flake check
diff -r extensions.done extensions
```

`nix flake check` ends with `all checks passed!`. The diff shows what you left out, such as the
comments and the other tests of the validator. Once you are done comparing, remove the copy:

```sh
git rm -r extensions.done
```

## Try it on a node

Build the image and check the nodes' files against it, then upgrade one node first:

```sh
nix build .#node-metal -o images/node-metal
chalkctl validate
chalkctl upgrade --nodes node1 --secrets=secrets.age
```

The node checks its config with the new image's validators, renderers and checks before it
installs anything, and boots the image with bird. [Test and roll out an image](../build/test-and-roll-out.md)
covers the rollout to the other nodes. Change the documents alone, such as a new `Peer`, with
`chalkctl config apply`, which needs no new image.

## Debug on the node

The tests catch what the image makes of a config. What remains are the node's own state and the
network: an interface, a password, a neighbour that does not answer. Each part of the extension
shows its state in another place:

| Part | Where it shows |
| --- | --- |
| Kinds and rules | `chalkctl validate`, and the refusal of `chalkctl config apply --dry-run` |
| Validator | the refusal of `chalkctl config apply --dry-run` |
| Output | `chalkctl status`, and the journal of the render's unit |
| bird and `bird-credentials` | `chalkctl logs` of `bird.service` |
| Effect | the output of `chalkctl config apply`, and bird's journal |
| Sessions and routes | `chalkctl node diag` |

### A delivery is refused

Ask the node to check a delivery without making it:

```sh
chalkctl config apply node1 --secrets=secrets.age --dry-run
```

It prints the refusal a delivery would get, with the document and field, such as the validator's:

```text
chalkctl: node1: the config is refused: Peer/router: spec.interfaces[2]: enp3s0 has no Network document; bird binds the session to an interface the node configures
```

Fix the file and check again. To find out why a config is refused, copy the node's documents into
a test's config file: the renderers and validators read nothing but the documents, so the test
gets the refusal the node gets, and `nix build -L` of the test results shows what the programs
printed.

### An output fails

`chalkctl status node1 --secrets=secrets.age` lists the outputs, `bird-conf` among them, each with
the hash of the files the node serves and its state. `failed:` names the last failure. The refusal
names the output and the exit status alone, as in the tests. The render ran as a transient unit,
`chalkos-render-<n>.service`, which chalkd's journal names beside the failure, and that unit's
journal holds what the renderer or the check printed:

```sh
chalkctl logs node1 --secrets=secrets.age --unit=chalkd.service
chalkctl logs node1 --secrets=secrets.age --unit=chalkos-render-<n>.service
```

A node renders every output again at boot, from the config it boots with. A renderer reads its
input alone, so the files at boot are those the delivery checked, and a test of the node's
documents renders the same `bird.conf` the node serves.

### bird does not run

bird's journal shows its start, each reload and the errors of `bird-credentials`:

```sh
chalkctl logs node1 --secrets=secrets.age --unit=bird.service
```

`bird takes no password with a quote or a line break` needs another value in the `Secret`.
`chalkctl status` names a unit that failed as `failed unit bird.service`. `--follow` keeps printing
new entries while you deliver a change, so a reload shows as it happens.

### A session does not come up

The diagnostic shows bird's sessions, each named after its `Peer` and interface:

```console
$ chalkctl node diag node1 --secrets=secrets.age bird-protocols
...
Name           Proto      Table      State  Since         Info
...
router_enp1s0  BGP        ---        up     10:02:16.731  Established
router_enp2s0  BGP        ---        up     10:02:16.902  Established
```

A session in `Connect` or `Active` reaches no neighbour. `protocol=router_enp1s0` shows one session
alone. The base diagnostics show the rest of the path: `addresses` that the ports and `node0` hold
their addresses, `neighbours` that the router answers on the port, and `routes` that bird installed
the default route with a next hop per session:

```sh
chalkctl node diag node1 --secrets=secrets.age addresses
chalkctl node diag node1 --secrets=secrets.age neighbours
chalkctl node diag node1 --secrets=secrets.age routes family=inet6
```

The kernel drops TCP segments whose MD5 signature does not match, so a wrong password logs nothing
on either side: the session stays in `Connect` or `Active`. Compare the passwords of the two sides.
[A BGP session does not come up](home-lab.md#a-bgp-session-does-not-come-up) of the home lab lists
the other causes. `not_found: the image declares no diagnostic "bird-protocols"` means the node
still runs an image without it.

## Check that it worked

`nix flake check` passes, `chalkctl validate` prints a line per node, and on the upgraded node
`chalkctl status` lists `bird-conf` as `rendered` and `bird-protocols` shows both sessions
`Established`.

## What next

- [Test an extension](../build/test-an-extension.md) lists every field of a test.
- [Read config in a service](../build/read-config.md) gives a unit the documents themselves, for a
  service that reads JSON.
- [The config pipeline](../how-it-works/node/config-pipeline.md) shows where the schemas, rules,
  validators, outputs and effects run in a delivery.
- The [extension API](../reference/extension-api.md) lists every option this page used.
