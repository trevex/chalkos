---
title: "Write a BGP extension"
description: "Build a BIRD BGP extension from an empty module, test each part, and debug it on a node"
---

# Write a BGP extension

This tutorial builds the BGP extension in
[`examples/bgp-extension`](https://github.com/trevex/chalkos/tree/main/examples/bgp-extension)
from an empty module. Its image, `node-bird-metal`, uses BIRD in place of the Kubernetes layer's
[built-in BGP speaker](../kubernetes/bgp.md). The extension
runs bird on each node, with two config kinds of its own: `Speaker` for the node's side of its
sessions and `Peer` for its neighbours. At the end `node-bird-metal` declares both kinds,
renders `bird.conf` from them, runs bird with the sessions' password, reloads it on a change,
refuses config bird cannot run, and shows bird's sessions to an operator. Each step adds one part,
shows how to look at what it produced, and tests it before the next. The last part debugs the
extension on a running node. Plan about an hour, most of it for builds.

An extension adds to an image, so this tutorial builds images of your own: a published image
holds only the layers chalkos ships, as
[Published images or your own](../get-started/decisions.md#published-images-or-your-own)
explains.

For a smaller first step, [Write your first extension](../build/first-extension.md) adds one kind,
a rendered file, a test and a unit to the quick start's lab in about 15 minutes.

The [Build images](../build/index.md) pages explain each part in depth. This page puts them in the
order of the work and links to them where the details matter.

## Before you begin

You need:

- a chalkos checkout and the homelab example in a git repository of its own, with the flake's shell, as
  [Prepare the files](home-lab.md#prepare-the-files) and [Get the tools](home-lab.md#get-the-tools)
  of the home lab set it up. Run every command of this page in that shell, in the repository;
- some Nix: NixOS modules, options and `let` bindings;
- for the last part, a node of the home lab, which [Try it on a node](#try-it-on-a-node) moves to
  `node-bird-metal`. [Build a home lab](home-lab.md) installs three. The extension needs a BGP
  neighbour on an IPv6 network, which the quick start's lab of virtual machines lacks, so that lab
  can run the steps up to the node but not the sessions.

The steps up to [Try it on a node](#try-it-on-a-node) need no node: they run in the Nix sandbox.

## Start from an empty module

Keep a copy of the finished extension nearby while you build your own. These commands assume
you made the homelab beside the chalkos checkout as shown in the home lab guide.

```sh
cp -r ../chalkos/examples/bgp-extension/extensions extensions.done
mkdir extensions
```

Add this entry to `images` in the homelab's `flake.nix`. It builds `node-bird-metal` with your
`extensions/` directory and turns off the Kubernetes layer's built-in BGP in that image.

```nix
--8<-- "examples/bgp-extension/flake.nix:bird-image"
```

Add `node-bird-metal` to the list inherited from `images` in `packages.${system}`. Create
`extensions/default.nix` to import the extension:

```nix title="extensions/default.nix"
--8<-- "examples/bgp-extension/extensions/default.nix"
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
git add flake.nix extensions
```

A file that git does not track is missing from the build, and the error names a path that does not
exist. Run `git add` after each new file of the steps below.

## Declare the kinds

A kind is the schema of a config document. The image converts its options into a JSON Schema that
chalkctl and chalkd check every document against. Add `Peer` to the module's body:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:kind"
```

Each field is a NixOS option. The type decides what the schema accepts, so pick the narrowest one:
every value here lands in `bird.conf`, and `chalkosLib.types.address` and `interfaceName` keep a
value from adding a statement of its own. Without them, a value such as
`fdc8:a1b2:c3d4:1::1; import all` would add a statement to `bird.conf`. The rule of `address`
keeps it to IPv6, and the pattern of `interfaceName` keeps each name of `interfaces` to the
characters of an interface name. A field without a default is required. `password` and `bfd`
have defaults, so a `Peer` may leave both out. `rules` and `unique` need `chalkosLib.mkOption` in
place of `lib.mkOption`.

Add `Speaker`, of which a node holds one, since it has no `multiple`:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:speaker"
```

chalkctl refuses a second `Speaker` on a node. `export.interfaces` is a nested field: an attribute
set of options in `spec` becomes an object of the schema. The rule of `routerID` refuses
`0.0.0.0`, which the pattern of an IPv4 address accepts. Each node needs a router ID of its own,
so its `Speaker` sits in a file of that node alone. node1's holds the fields of its built-in
`BGPSpeaker`:

```yaml
apiVersion: bgp.example.org/v1alpha1
kind: Speaker
metadata: {name: bgp}
spec:
  asn: 65001
  routerID: 0.0.0.11
  export: {interfaces: [node0]}
```

### Look at the schemas

The schemas are a build output of their own, which takes seconds and needs no image. Build them
and print a field:

```console
$ nix build .#nixosConfigurations.node-bird-metal.config.system.build.chalkosSchemas
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

### Validate a node's files

The homelab's files hold the built-in `BGPSpeaker` and `BGPPeer` documents, which
`node-bird-metal` does not accept. Give node1 a file of its own with the extension's documents in
their place. Start from its current file:

```sh
cp nodes/node1.yaml nodes/node1-bird.yaml
```

In `nodes/node1-bird.yaml`, replace the `BGPSpeaker` document with the `Speaker` above, and add
the ports' `Network` documents of `bgp/fabric.yaml` and a `Peer` for the router. bird runs a
session on each interface of the `Peer`, both to the router's first address. The file's header
comment and the comment above the `Speaker` speak of FRR, `BGPPeer` documents and
`bgp/fabric.yaml`: adapt them to bird and to the documents the file now holds.

```yaml
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-enp1s0}
spec:
  match: {name: enp1s0}
  network: {ipv6AcceptRA: true}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-enp2s0}
spec:
  match: {name: enp2s0}
  network: {ipv6AcceptRA: true}
---
apiVersion: bgp.example.org/v1alpha1
kind: Peer
metadata: {name: router}
spec:
  address: "fdc8:a1b2:c3d4:1::1"
  asn: 65000
  interfaces: [enp1s0, enp2s0]
  password: {secretRef: {name: bgp, key: password}}
```

In `chalkos.yaml`, add the image and give node1 that image and its files, outside the group
`nodes`, whose `bgp/fabric.yaml` holds `BGPPeer` documents:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    router-metal: {source: {directory: images/router-metal}}
    node-metal: {source: {directory: images/node-metal}}
    node-bird-metal: {source: {directory: images/node-bird-metal}}
  groups:
    router: {imageName: router-metal, files: [router/*.yaml]}
    nodes: {imageName: node-metal, files: [cluster/*.yaml, bgp/fabric.yaml, bgp/secrets.yaml]}
  nodes:
    router: {address: 192.168.246.2, groups: [router]}
    node1:
      address: "fdc8:a1b2:c3d4:ff::11"
      imageName: node-bird-metal
      files: [cluster/*.yaml, bgp/secrets.yaml, nodes/node1-bird.yaml]
    node2: {address: "fdc8:a1b2:c3d4:ff::12", groups: [nodes], files: [nodes/node2.yaml]}
    node3: {address: "fdc8:a1b2:c3d4:ff::13", groups: [nodes], files: [nodes/node3.yaml]}
```

`chalkctl validate` checks each node's documents against the schemas of the image the inventory
names. It reads the built image, so build it first:

```sh
nix build .#node-bird-metal -o images/node-bird-metal
chalkctl validate
```

The image build takes minutes, so keep it for the files of the nodes. With `asn` misspelled as
`asm` in the `Peer`, `chalkctl validate` refuses node1:

```console
$ chalkctl validate
...
chalkctl: the config of 1 node is invalid:
node node1: Peer "router": - at '/spec': additional properties 'asm' not allowed
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
--8<-- "examples/bgp-extension/extensions/bird.conf.tmpl"
```

The template is Go's `text/template`. It reads the node's name as `.node.name` and each input
kind's documents as a list, `.documents.Speaker` and `.documents.Peer`, sorted by name and with
the schemas' defaults filled in. `quote` and `refuse` are chalkos's own functions. The
template's first lines refuse `Peer` documents on a node without a `Speaker`, whose AS number
every session needs: `refuse` prints a refusal that names the first `Peer`, and the delivery is
refused.

The output's check runs bird's parser on the rendered file, so the module needs bird's package.
Add it to the `let` bindings:

```nix
bird = config.services.bird.package;
```

Then declare the output in the body:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:output"
```

The check is a program, which chalkd runs without a shell. bird needs a constant for each password
line, so the check gives `bird -p` a config that defines them and then includes `bird.conf`.
Create `extensions/bird-conf-check.jq`, which writes that config from the rendered file:

```text title="extensions/bird-conf-check.jq"
--8<-- "examples/bgp-extension/extensions/bird-conf-check.jq"
```

The check is a script for execline, a language without a shell: it pipes jq's output into bird,
which reads the config from standard input. A check that only runs a program on the files needs
no script:
`chalkosLib.check.run [ "${bird}/bin/bird" "-p" "-c" "$OUT/bird.conf" ]` would do for a config
without passwords.

On a node, chalkd renders and checks every output before a delivery changes anything, and refuses
the delivery when one fails. The same renderer and check run in the image's tests, without a node.

### See what the output renders

A test gives the image a config and records what the image makes of it. Copy the example config of
a homelab node that runs the extension, which holds a `Speaker`, the `Peer`, the `Secret` with the
password and the `Network` documents of the ports, and declare a test of it in the body:

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
$ nix build .#nixosConfigurations.node-bird-metal.config.system.build.chalkosTestResults
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

```console
$ cat result/bgp-fabric/outputs/bird-conf/bird.conf
--8<-- "examples/bgp-extension/extensions/tests/fabric/bird-conf/bird.conf"
```

It holds a `protocol bgp` per interface of the `Peer`, named after the `Peer` and the interface
and bound to that interface. The `Speaker` gives the router ID, the AS number of every session
and the interfaces of `node_addresses`, whose addresses each session announces. If a neighbour
sends a default route, the extension accepts it and `merge paths` can install it with a next hop
per session. The homelab router sends its gateway in router advertisements. The test's node is
named `example`. After
each change to the template, build the results again and read the new file.

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
$ nix build -L .#nixosConfigurations.node-bird-metal.config.system.build.chalkosTestResults
...
chalkos-test-results> 2026/11/16 07:52:35 output bird-conf: its check /nix/store/39kg0jfg9wiagx56jrf3hpnwjvh6xv44-bird-conf-check failed (exit 1): "bird: 2026-11-16 07:52:35.151 [0000] <FATAL> /build/chalkos-tests-2581098952/.staging/bird-conf.1551592570/bird.conf:4:1 syntax error, unexpected PROTOCOL, expecting ';'"
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
command that takes the new file.

## Run bird

The image needs bird itself, reading the rendered file. Add the service to the body:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:service"
```

`bird.conf` exists on the node alone, so the NixOS module's check of the config is off, and the
output's check takes its place. The include of the output's `dir` names the output by its option,
so a misspelled name fails the evaluation. The service runs in chalkos's sandbox,
[`chalkosLib.sandbox.hardened`](../reference/extension-api.md#chalkoslibsandboxhardened), with
the capabilities nixpkgs grants bird. Its settings run `bird-credentials`, which turns the
sessions' password into bird constants. Add it to the `let` bindings:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:credentials"
```

Create `extensions/bird-credentials.rs`, the program. `pkgs.writers.writeRustBin` compiles it
when the image builds, and a compiled program needs no shell:

```rust title="extensions/bird-credentials.rs"
--8<-- "examples/bgp-extension/extensions/bird-credentials.rs"
```

`bird-credentials` runs before bird starts and on each reload. It reads each `secret_*` file in
`$CREDENTIALS_DIRECTORY` and writes a line `define 'secret_bgp.password' = "...";` to
`/run/bird/credentials.conf`, which only bird's user may read. bird's config includes that file
before the rendered one. chalkd accepts more characters in a `Secret`'s name and key than
`bird-credentials` does, so keep both to letters, digits, `.`, `_` and `-`.

A renderer never sees a secret's value, so `bird.conf` names the password by its credential,
`'secret_bgp.password'`, and the constant resolves it. A grant of the `Peer` kind has chalkd hand
bird the values that the `Peer` documents' `password` fields name, as systemd credentials:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:grant"
```

The homelab keeps the password in `bgp/secrets.yaml`, a file of its own, so that the file with
the `Peer` can go into a public repository. The router's side of the sessions needs the same
password, which `router/secrets.yaml` holds in a `Secret` of the router's own.

Last, two settings outside bird: networkd leaves the routes bird installs alone, and the
firewall lets BFD's packets in:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:network"
```

Build the image. The build checks what the evaluation cannot, such as a granted unit that runs
nothing:

```sh
nix build .#node-bird-metal -o images/node-bird-metal
```

## Reload bird on a change

A delivery that changes `bird.conf` must reach bird. An effect says what a delivery does to a
service once it is applied. Add it to the body:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:effect"
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
--8<-- "examples/bgp-extension/extensions/bgp.nix:tests"
```

`bird.conf` names the credential, not its value, so the new password leaves the file as it was,
and the test compares it with the same golden files. The effect fires all the same, and the
results show it:

```console
$ cat result/bgp-new-password/effects.json
["bird"]
```

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
--8<-- "examples/bgp-extension/extensions/bgp.nix:validator"
```

The program reads the same input as a renderer, and prints a report of refusals, one per
interface that no `Network` names:

```text title="extensions/bgp-peers.jq"
--8<-- "examples/bgp-extension/extensions/bgp-peers.jq"
```

A section of a networkd document may be one object or a list of them, and a key one value or a
list, so the program reads `name` from every `[Match]` section, and splits a value that holds
several names. It compares the names as written. A glob such as `enp*` may or may not match an
interface, so the program takes it for no name, and a `Peer` on such an interface is refused.

The program also refuses `Peer` documents without a `Speaker`, as the template does. The
validators run before the renderers, so the operator sees the validator's refusal.
Test this one with a config whose `Peer` names a third interface, `enp3s0`, without a `Network`
document:

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

`result/bgp-unconfigured-peer-interface/refusals.json` holds what the test got:

```console
$ cat result/bgp-unconfigured-peer-interface/refusals.json
[{"kind":"Peer","name":"router","path":"spec.interfaces[2]","message":"enp3s0 has no Network document; bird binds the session to an interface the node configures"}]
```

`chalkctl validate` accepts this config, because chalkctl runs no validator of the image. The node
refuses it on delivery.

The finished extension tests the validator on more configs: interfaces matched by lists, globs and
names separated by spaces, a `Peer` without a `Speaker`, a `Speaker` that exports an interface
without a `Network` document and a node without any `Peer`. It also tests the example config,
`bgp-examples/fabric.yaml`, which the validator accepts. It declares the tests of `tests/`
through a function of its own, `validatorTest`, that takes a file's name and the refusals. Cover each branch of a program
this way: a config it accepts, and one per refusal.

## Add a diagnostic

A node has no logins, so an operator reads bird's state through a diagnostic, a read-only command
of the image that chalkd runs on request. Add it to the body:

```nix
--8<-- "examples/bgp-extension/extensions/bgp.nix:diagnostic"
```

`birdc -r` asks bird in its read-only mode, through bird's socket, which `groups` makes reachable.
`protocol` takes only names of the form `bird.conf` gives the sessions, so no value closes bird's
quotes. The image's build checks the declaration: a placeholder without a parameter, or a group the
image lacks, fails it.

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
rm -r extensions.done
```

## Try it on a node

Build the image and check node1's files against it, as in
[Validate a node's files](#validate-a-nodes-files), then move node1 to the image with an upgrade,
which carries node1's files:

```sh
nix build .#node-bird-metal -o images/node-bird-metal
chalkctl validate
chalkctl upgrade node1 --secrets=secrets.age
```

The node checks its config with the new image's validators, renderers and checks before it
installs anything, and boots the image with bird in place of the built-in speaker. Change the
documents alone, such as a new `Peer`, with `chalkctl config apply`, which needs no new image.

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

`chalkctl status node1` lists the outputs, `bird-conf` among them, each with
the hash of the files the node serves and its state. `failed:` names the last failure. The refusal
names the output and the exit status, as in the tests, and ends with the command that shows the
journal of the transient unit the render ran as, `chalkos-render-bird-conf-<n>.service`. That
journal holds what the renderer or the check printed:

```sh
chalkctl logs node1 --unit=chalkos-render-bird-conf-<n>.service
```

A node renders every output again at boot, from the config it boots with. A renderer reads its
input alone, so the files at boot are those the delivery checked, and a test of the node's
documents renders the same `bird.conf` the node serves.

### bird does not run

bird's journal shows its start, each reload and the errors of `bird-credentials`:

```sh
chalkctl logs node1 --unit=bird.service
```

`bird takes no password with a quote or a line break` needs another value in the `Secret`.
`bird takes a credential name of letters, digits, '.', '_' and '-' alone` needs a `Secret` name
and key of those characters. `chalkctl status` names a unit that failed as `failed unit bird.service`. `--follow` keeps printing
new entries while you deliver a change, so a reload shows as it happens.

### A session does not come up

The diagnostic shows bird's sessions, each named after its `Peer` and interface:

```console
$ chalkctl node diag node1 bird-protocols
...
Name           Proto      Table      State  Since         Info
...
router_enp1s0  BGP        ---        up     10:02:16.731  Established
router_enp2s0  BGP        ---        up     10:02:16.902  Established
```

A session in `Connect` or `Active` reaches no neighbour. `protocol=router_enp1s0` shows one session
alone. The base diagnostics show the rest of the path: `addresses` that the ports and `node0` hold
their addresses, `neighbours` that the router answers on the port, and `routes` that the node
has a default route with protocol `ra`:

```sh
chalkctl node diag node1 addresses
chalkctl node diag node1 neighbours
chalkctl node diag node1 routes family=inet6
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
- [Develop and debug an extension](../build/develop-an-extension.md) runs the checks from the
  fastest build to a node and lists the common errors.
- [Read config in a service](../build/read-config.md) gives a unit the documents themselves, for a
  service that reads JSON.
- [The config pipeline](../how-it-works/node/config-pipeline.md) shows where the schemas, rules,
  validators, outputs and effects run in a delivery.
- The [extension API](../reference/extension-api.md) lists every option this page used.

These pages explain the steps of this tutorial in more depth:

- [Field types](../reference/extension-runtime.md#field-types) lists the option types and what
  each becomes in the schema.
- [Template functions](../reference/extension-runtime.md#template-functions) lists `quote`,
  `refuse` and the other functions of a template.
- [A failing test shows what it got](../build/test-an-extension.md#a-failing-test-shows-what-it-got)
  shows how a test prints the command that takes new golden files.
- [Pass secrets to a service](../build/pass-secrets.md) covers `secretRef` fields, credentials and
  rotation.
- [React to changes](../build/react-to-changes.md) covers restarts, commands and conditions.
- [Guard changes with rules and facts](../build/guard-changes.md) covers rules over several
  fields, over all of a kind's documents, and rules that read the node's state.
- [Validate config with a program](../build/validate-config.md) covers validators in general.
- [Add a diagnostic](../build/add-a-diagnostic.md) lists the parameter types and the sandbox the
  command runs in.
- [Test and roll out an image](../build/test-and-roll-out.md) covers the rollout to the other
  nodes.
