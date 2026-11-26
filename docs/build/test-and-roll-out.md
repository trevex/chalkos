---
title: "Test and roll out an image"
description: "Check an image and the nodes' config, upgrade the nodes, and confirm their outputs and services"
---

# Test and roll out an image

This page is for an image author whose changed image is ready to reach running nodes. At the end
the image has passed its build checks and its tests, every node's config has passed
`chalkctl validate`, the nodes run the image, and `chalkctl status` and `chalkctl logs` show that
the outputs rendered and the services read them. The examples use the `cluster-metal` template
with `hardware.nix` of the
[extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions) added to its
image `node-metal`: the kind `Hardware`, the output `hardware`, which
[Render config files](render-config.md) describes, and the unit `hardware-info`, which reads it.

A change reaches a node in one of two ways. A changed image reaches it with
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md), which brings the node's config along.
Changed YAML alone reaches it with
[`chalkctl config apply`](../reference/cli/chalkctl_config_apply.md), and needs no new image.

## Before you begin

You need:

- The template's flake and inventory, with `hardware.nix` in the `modules` of `node-metal` and a
  `Hardware` document in `nodes/cp1.yaml`, as [Declare a config kind](declare-a-kind.md)
  describes, and a cluster whose nodes run an earlier image.
- The secrets file to deliver config, here `secrets.age`. `chalkctl upgrade` also runs with an
  operator client file, and then sends no config.
- For nodes that enforce Secure Boot, a signed copy of the image, as
  [Sign images for Secure Boot](sign-images.md) makes it.

## The image build checks the image

Build the image:

```sh
nix build .#node-metal -o images/node-metal
```

The build fails before any node gets the image when one of these checks fails:

- Every rule of the image's kinds and effects compiles, stays within the cost limit, and reads
  only facts that a module declares.
- Every effect acts on units, outputs and kinds that the image defines.
- Every output and validator is a program of the image's store, and reads kinds the image
  declares.
- The store, its hash tree and the UKIs fit their partitions with room to spare, as
  [Add software, drivers and kernel modules](add-software.md#keep-the-image-within-its-slot)
  describes.

The error names the rule, the effect, the output or the validator.

## nix flake check runs the image's tests

The image's tests, `chalkos.tests`, run each test's config through the checks a node runs for a
delivery: they render every output and run its check, run the validators, and compare the files,
refusals and effects with what each test expects. The template's flake exports them as its checks,
so one command runs every image's tests:

```sh
nix flake check
```

It fails on a test that fails, with a diff for each output whose files differ from the test's
golden files. `hardware` renders `hardware-examples/server.yaml` in the test `hardware-server`,
and the test compares the result with its golden `hardware.env`. The image's build runs
the tests only with `chalkos.testsOnBuild`. [Test an extension](test-an-extension.md) declares the
tests and covers the documents nodes hold.

## chalkctl validate checks each node's config

`chalkctl validate` checks each node's documents against the schemas of the image the inventory
names for it. It also runs the image's rules that read no facts, and reaches no node:

```console
$ chalkctl validate
cp1: valid for the image node-metal (node-metal on metal, root hash ae887bd49725), config 03694425cf0db40d0fbbe558ec86f70411dd93bd465c4eed8a5c8342808bc09c
cp2: valid for the image node-metal (node-metal on metal, root hash ae887bd49725), config 10c35f7fe988679c3a54691ddf95e78267269d22297fb360ceae5527f5965d50
cp3: valid for the image node-metal (node-metal on metal, root hash ae887bd49725), config 55ed0b193224383f68d0bb4014e45885697279cd67dfa6fc9232627757cfbd44
```

Each line names the image as the inventory names it, the name and platform it was built with, the
start of its root hash, and the hash of the config the node would get. A `Hardware` with `model`
misspelled as `modle` fails on every node that gets the file, here cp1 alone:

```console
$ chalkctl validate
...
chalkctl: the config of 1 node is invalid:
node cp1: Hardware "hardware": - at '/spec': additional properties 'modle' not allowed
- at '/spec': missing property 'model'
```

Rules that compare with the config a node runs and rules that read facts, such as a rule that
compares a `Rack`'s location with the node's survey, run when the config reaches the node.
[Guard changes with rules and facts](guard-changes.md) describes where each rule runs.

## Roll the image out with chalkctl upgrade

`chalkctl upgrade` gives each node the image the inventory names for it:

```sh
chalkctl upgrade --secrets=secrets.age
```

Nodes that run the image already are skipped. Each other node gets its config with the image: the
documents of the files the inventory gives it, checked against the new image before any node gets
the image. The node checks them again and keeps them for that image, so a fallback boots the image
before with its own config. Before it sends a node anything, chalkctl prints a line of what the
node's config changes, such as `cp1: config unchanged`.

The node has the new image check its config before it installs it, with that image's own
renderers and their checks. A config one of them refuses refuses the upgrade of that node, and
the run stops there. The image renders its outputs again at its first boot.

[Upgrade nodes](../operate/upgrade.md) covers the run's order, downtime and
rollbacks.

## Deliver changed YAML with chalkctl config apply

A change to the documents alone, such as a new asset tag in the `Hardware`, needs no upgrade.
`--diff` shows what a delivery changes in the documents. `--dry-run` has the node check the
delivery, its renderers and their checks included, and print the effects it would take. Neither
changes anything:

```sh
chalkctl config apply cp1 --secrets=secrets.age --diff
chalkctl config apply cp1 --secrets=secrets.age --dry-run
chalkctl config apply cp1 --secrets=secrets.age
```

The node renders and checks every output before it changes anything, so a render or a check
that fails refuses the whole delivery. chalkctl prints each unit the effects restarted, as
`restarted hardware-info.service`, and then the config the node runs, as `cp1 runs config <hash>`.
Reloaded units are not listed.

After an upgrade with `--until stage` or `--until activate`, the image in the node's inactive slot
keeps a config of its own, staged or active. `chalkctl config apply` delivers to that image as
well as to the running one, once both checked the config, and `chalkctl config apply --staged`
delivers to that image alone. With `--dry-run`, both check the config and nothing changes.

Config for an image that is not installed yet can wait on the node. Without a staged image,
`chalkctl config apply --staged` holds the config pending, and the next upgrade gives it to the
new image, which checks it before the node installs anything. An operator can then run that
upgrade without the secrets file.

## chalkctl status shows the outputs

`chalkctl status` shows the image a node booted and whether the boot was found healthy. It also
lists each output the node serves:

```sh
chalkctl status cp1
```

The outputs form a table with three columns:

- `OUTPUT` is the output's name, such as `hardware`.
- `HASH` is the start of the hash of the files the node serves, or `none` while it serves none.
- `STATE` is `rendered`, `failed:` with the last failure, or `not rendered yet`.

A node whose store is not the image the inventory names gets a line that starts with
`its store's root hash is`, and that names the command that fixes it. A unit that failed is named
as `failed unit <unit>`.

## chalkctl logs shows a unit's journal

`chalkctl logs` prints a node's journal of the current boot, for one unit with `--unit`:

```sh
chalkctl logs cp1 --unit=hardware-info.service
```

A render or a check runs as a transient unit, `chalkos-render-<output>-<n>.service`, which keeps
the program's standard error. The failure ends with the command that shows that unit's journal:

```sh
chalkctl logs cp1 --unit=chalkos-render-<output>-<n>.service
```

`--follow` keeps printing new entries until you interrupt it.

## What next

- [Upgrade nodes](../operate/upgrade.md) rolls an image through a cluster node by node.
- [The config pipeline](../how-it-works/node/config-pipeline.md) explains what a node does with a
  delivery, at boot and after an upgrade.
- [Troubleshooting](../operate/troubleshooting.md) starts from `chalkctl status` when a node
  misbehaves.
