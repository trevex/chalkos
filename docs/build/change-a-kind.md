---
title: "Change a kind incompatibly"
description: "Give a kind a new version, move the nodes' documents to it with an upgrade, and rename or remove a kind"
---

# Change a kind incompatibly

This page is for an image author whose change to a kind would refuse documents the nodes already
hold. At the end the kind has a new version, the nodes' files hold documents of it, and every node
runs the new image with them. The image before keeps the old documents, so a rollback still boots
with config it accepts. The example gives the `Hardware` kind of the
[extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions) a required
field, `serial`, in the version `v1alpha2`.

In short:

```sh
# Change the kind's version and its documents in one commit, then:
nix flake check
nix build .#node-metal -o images/node-metal
chalkctl validate
chalkctl upgrade --nodes=server1 --diff --secrets=secrets.age
chalkctl upgrade --secrets=secrets.age
```

## Before you begin

You need:

- The flake that declares the kind, as [Declare a config kind](declare-a-kind.md) writes it, and
  its inventory with the nodes' files.
- Nodes that run an image with the kind's current version.
- The secrets file or an admin client file. An upgrade carries the nodes' config only with one of
  them. [Hold the documents pending for an operator](#hold-the-documents-pending-for-an-operator)
  covers an upgrade with an operator client file.

## Decide whether the change needs a new version

An image declares one version of each kind, and chalkos converts no document from one version to
another. Changes that keep existing documents valid need no new version:

- a new field with a default;
- a looser type, such as a wider integer range.

Other changes refuse documents the nodes already hold. A new required field, a removed or renamed
field and a narrower type are among them. Make such a change a new `version`, here `v1alpha2`.
The new image then accepts documents of `v1alpha2` alone, and the image before accepts `v1alpha1`
alone.

## Change the module and the documents in one commit

Raise the kind's `version` and change its fields:

```diff title="hardware.nix"
   chalkos.kinds.Hardware = {
     group = "hardware.example.org";
-    version = "v1alpha1";
+    version = "v1alpha2";
     description = "Who made the node's server and which model it is.";
     spec = {
       ...
+      serial = lib.mkOption {
+        type = lib.types.nonEmptyStr;
+        example = "S123456X7890123";
+        description = "Serial number of the node's server.";
+      };
```

Change everything that reads the field, such as the template of the output `hardware`:

```diff title="hardware.env.tmpl"
 MODEL={{ quote .spec.model }}
+SERIAL={{ quote .spec.serial }}
```

Then move every document of the kind to the new version, in the nodes' files and in the tests'
config files:

```diff title="nodes/server1.yaml"
-apiVersion: hardware.example.org/v1alpha1
+apiVersion: hardware.example.org/v1alpha2
 kind: Hardware
 metadata: {name: hardware}
-spec: {vendor: Supermicro, model: SYS-E300-9D, assetTag: HL-0001}
+spec: {vendor: Supermicro, model: SYS-E300-9D, serial: S123456X7890123, assetTag: HL-0001}
```

Commit the module and the documents together. Each commit then builds an image that accepts its
own files, and reverting the commit brings back both halves.

## Run the tests

A test's config file still in the old version fails its test. `chalkosTestResults` shows how:

```console
$ nix build .#nixosConfigurations.node-metal.config.system.build.chalkosTestResults
$ cat result/summary.txt
PASS chrony-empty
PASS chrony-servers
FAIL hardware-server: output hardware rendered nothing; the config was refused
FAIL hardware-server: refusals differ: expected [], got [{"kind":"","name":"","path":"","message":"does not accept kind \"Hardware\" of apiVersion \"hardware.example.org/v1alpha1\""}]
PASS networkd-none
PASS networkd-static
PASS networkd-vlan
```

Move the file to `v1alpha2`, update the golden files for the new field, and run every test:

```sh
nix flake check
```

The tests belong to one image, so they cannot check the step from one version to the next. A
test whose `before` holds documents of the old version fails, because the new image refuses them:

```text
FAIL hardware-from-v1alpha1: before is refused: does not accept kind "Hardware" of apiVersion "hardware.example.org/v1alpha1"
```

`chalkctl validate` and the upgrade check that step instead. A `before` of the new version still
tests the kind's change rules and effects, as [Test an extension](test-an-extension.md) shows.

## Validate the nodes' files

Build the image and check every node's files against it:

```console
$ nix build .#node-metal -o images/node-metal
$ chalkctl validate
server1: valid for the image node-metal (node-metal on metal, root hash 2f5a05382bf9), config 1451fbf176cfc8cacba9f488de65a5db77fd5005a5d0715f613892d5b48db7c8
```

A file that still holds the old version names the version the image refuses:

```text
chalkctl: the config of 1 node is invalid:
node server1: does not accept kind "Hardware" of apiVersion "hardware.example.org/v1alpha1"
```

A document of the new version without the new field names the field:

```text
chalkctl: the config of 1 node is invalid:
node server1: Hardware "hardware": - at '/spec': missing property 'serial'
```

## Upgrade the nodes

Preview the change on one node first:

```sh
chalkctl upgrade --nodes=server1 --diff --secrets=secrets.age
```

chalkctl compares documents by `apiVersion`, kind and name. The diff therefore shows the document
of the old version as removed and that of the new version as added:
`server1: config 1 added, 1 removed (Hardware "hardware"), 0 changed`.

Then upgrade the nodes:

```sh
chalkctl upgrade --secrets=secrets.age
```

Before it changes any node, chalkctl checks every node's files against the new image. Each node
gets its documents with the image. The new image checks them with its own checker, and the node
keeps them in the new image's config slot. The slot of the image before keeps the documents of
`v1alpha1`. [Upgrade nodes](../operate/upgrade.md) covers the run's order, batches and flags.

The new image's change rules see no document of the old version. To the new image, a document of
a version it does not accept is absent, so a rule that reads `oldSelf` treats the document as
new. A rule that keeps a field's value therefore does not hold across the version change.

## Hold the documents pending for an operator

An upgrade with an operator client file sends no config. Unless the node holds a pending config,
it carries the running image's documents over, and the new image refuses those of the old version:

```text
the node's config does not suit the image: ...; upgrade with config the image accepts, from an inventory, which needs the admin role
```

Someone with the secrets file can deliver the new documents ahead of the upgrade. On a node
without a staged image, `--staged` holds them as the node's pending config:

```sh
chalkctl config apply server1 --staged --secrets=secrets.age
```

chalkctl checks the files against the node's image in the inventory, the new image. The node holds
the config unchecked, and the next upgrade gives it to the new image, which checks it. An
operator's upgrade then brings the new documents along. A pending config the new image refuses
stops the upgrade with `the pending config <version> does not suit the image: ...`. That error names
`chalkctl config apply --staged <node>`, which replaces the pending config, and
`chalkctl config discard <node>`, which drops it.

## Deliver config to a staged image

After `chalkctl upgrade --until stage` or `--until activate`, the new image waits in the node's
inactive slot with its own config. A plain `chalkctl config apply` checks the files against the
running image first, which refuses the new version. Deliver to the staged image alone:

```sh
chalkctl config apply server1 --staged --secrets=secrets.age
```

The command ends with `server1 boots config <version> with its staged image`.
[Deliver config to a staged image](../operate/change-config.md#deliver-config-to-a-staged-image)
covers both deliveries.

## Mind the nodes that still run the old image

The inventory gives each node the files of the current commit, whatever image the node runs. A
node that still runs the old image gets the new documents too. This happens between two nodes of
a run, and after a run that stopped or a node that rolled back. Such a node behaves this way:

- `chalkctl validate` passes, because it checks against the image in the inventory.
- `chalkctl config apply` refuses before it sends anything, because it checks against the image
  the node runs:
  `the config of <node>: does not accept kind "Hardware" of apiVersion "hardware.example.org/v1alpha2"`.
- `chalkctl status` shows its config and its image as not the inventory's. The image line names
  the command that brings the node to the inventory's image, such as
  `chalkctl upgrade --nodes <node>`, which fixes both. The config line names the same upgrade:
  `not the inventory's; chalkctl upgrade --nodes <node> delivers it with the image <image>`.

To see what such a node accepts, validate against the old image. Here `chalkos-old.yaml` is a copy
of the inventory whose `spec.images` names a build of the commit before:

```console
$ chalkctl validate --inventory chalkos-old.yaml
chalkctl: the config of 1 node is invalid:
node server1: does not accept kind "Hardware" of apiVersion "hardware.example.org/v1alpha2"
```

Upgrade every node of the group before the next config change. A change that cannot wait reaches
an upgraded node with `chalkctl config apply`, and a node with the new image staged with
`--staged`.

## Roll back

A node whose new image is not found healthy falls back to the image before, and
`chalkctl upgrade --rollback` boots that image on request. Either way the image boots with its own
slot, which holds the documents of `v1alpha1`. Nothing needs to change in the files for it to
boot.

The rolled-back node then runs the old image with the new files in the inventory, as the
section before describes. `chalkctl config rollback` does not help: it restores the previous
config of the running image's slot, never documents of another image. To move on, fix the image
and upgrade again, as
[A node rolled back during the upgrade](../operate/upgrade.md#a-node-rolled-back-during-the-upgrade)
describes.

To stay on the old version, revert the commit. The revert brings back the module and the
documents of `v1alpha1` together. Build the image again. When nothing else changed since, it has
the root hash of the image before. `chalkctl upgrade` then skips the nodes that run it, and gives the
nodes that took the new image the old one with the old documents.

The slot of the image before lasts until the next upgrade, which removes the slot of every image
the node no longer boots.

## Rename a kind

A kind of another name is another kind, and so is a kind in another `group`. Rename it as you
change a version: the module and the documents in one commit, then an upgrade. Change every place
that names the kind:

- `chalkos.kinds.<Kind>` and the documents' `kind`;
- the `inputs` of outputs, the `kinds` of effects and the grants of units,
  `systemd.services.<unit>.chalkos.kinds`;
- `chalkosLib.types.ref` fields and `config.chalkos.kinds.<Kind>.get` calls;
- templates and programs that read `.documents.<Kind>`.

The image's evaluation fails on an option that still names the old kind, and names the option and
the kinds the image declares, as in
`chalkos.outputs.<output>.inputs: the image declares no kind <Kind>; it declares ...`. A template that still ranges over `.documents.<old name>` renders an empty file without an error.
The test's golden files catch it.

## Remove a kind

Remove the kind's declaration, and every option that names it, in the same commit as its
documents. A document of a removed kind fails `chalkctl validate` with
`does not accept kind "<Kind>" of apiVersion "<apiVersion>"`. The upgrade gives the new image the
files without the kind. The slot of the image before keeps its documents, so a fallback boots
with them.

## Check that it worked

`chalkctl status` shows each node's config and image as the inventory's:

```sh
chalkctl status server1
```

The config line ends in `(the inventory's)`, and no line starts with `its store's root hash is`.

## If it fails

- `does not accept kind "Hardware" of apiVersion "hardware.example.org/v1alpha1"` from
  `chalkctl validate` or `chalkctl upgrade`: a file still holds the old version. Move it to the new
  one.
- `the config of <node>: does not accept kind ... of apiVersion "hardware.example.org/v1alpha2"`
  from `chalkctl config apply`: the node still runs the old image. Upgrade it, or deliver to its
  staged image with `--staged`.
- `the node's config does not suit the image: ...` from an upgrade with an operator client file:
  the node carried the old documents over. Upgrade with the secrets file, or hold the documents
  pending first.
- `before is refused: does not accept kind ...` in a test: the test's `before` holds the old
  version. Move it to the new one.

## What next

- [Test and roll out an image](test-and-roll-out.md) covers the build checks, the tests and the
  rollout of any image change.
- [Upgrade nodes](../operate/upgrade.md) runs an upgrade node by node and handles a rollback.
- [Config and the inventory](../how-it-works/node/config.md#a-node-keeps-one-config-slot-per-image)
  explains the config slots and which image checks each one.
- [Guard changes with rules and facts](guard-changes.md) covers the change rules that compare
  with the running config.
