---
title: "Test and roll out an image"
description: "Check an image and the nodes' config, upgrade the nodes, and confirm their outputs and services"
---

# Test and roll out an image

This page is for an image author whose changed image is ready to reach running nodes. At the end
the image has passed its build checks and its tests, every node's config has passed
`chalkctl validate`, the nodes run the image, and `chalkctl status` and `chalkctl logs` show that
the outputs rendered and the services read them. The page uses the homelab and its BGP extension,
`extensions/bgp.nix`.

A change reaches a node in one of two ways. A changed image reaches it with
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md), which brings the node's config along.
Changed YAML alone reaches it with
[`chalkctl config apply`](../reference/cli/chalkctl_config_apply.md), and needs no new image.

## Before you begin

You need:

- The flake and inventory of [Your first image flake](first-image.md), and a cluster whose nodes
  run an earlier image.
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
refusals and effects with what each test expects. The homelab's flake exports them as its checks,
so one command runs every image's tests:

```sh
nix flake check
```

It fails on a test that fails, with a diff for each output whose files differ from the test's
golden files. `bird-conf` renders `bgp-examples/fabric.yaml` in the test `bgp-fabric`, `bird -p`
parses the result, and the test compares it with its golden `bird.conf`. The image's build runs
the tests only with `chalkos.testsOnBuild`. [Test an extension](test-an-extension.md) declares the
tests and covers the documents nodes hold.

## chalkctl validate checks each node's config

`chalkctl validate` checks each node's documents against the schemas of the image the inventory
names for it. It also runs the image's rules that read no facts, and reaches no node:

```console
$ chalkctl validate
node1: valid for the image node-metal (node on metal, root hash d871203debfe), config c2bef3c1242a847b16346d3e1f6de732a86832fa8294ce90e923bad30c85e6bf
node2: valid for the image node-metal (node on metal, root hash d871203debfe), config afa765d5e8029dbe3aea1a589d2dafdf260de42fff21692656891ddde062d567
node3: valid for the image node-metal (node on metal, root hash d871203debfe), config 719604db3fd2c0634df350fc6b7760bba57de0ec82ac3fb9a9b2dcf532cfdc43
router: valid for the image router-metal (router on metal, root hash e9be19557973), config a8d132f3d5517185654d573c02c543497ab42d0068131d25bbfe8405d5761093
```

Each line names the image, its variant and platform, the start of its root hash, and the hash of
the config the node would get. A `Peer` with `asn: 0`, below the kind's minimum of 1, fails on
every node that gets the file:

```console
$ chalkctl validate
chalkctl: the config of 3 nodes is invalid:
node node1: Peer "router": - at '/spec/asn': minimum: got 0, want 1
node node2: Peer "router": - at '/spec/asn': minimum: got 0, want 1
node node3: Peer "router": - at '/spec/asn': minimum: got 0, want 1
```

Rules that compare with the config a node runs, such as the `Peer` rule that keeps `asn`, and
rules that read facts run when the config reaches the node.
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
node's config changes, such as `node1: config unchanged`.

The node has the new image check its config before it installs it, with that image's own
renderers and their checks. A config one of them refuses refuses the upgrade of that node, and
the run stops there. The image renders its outputs again at its first boot.

[Upgrade nodes](../operate/upgrade.md) covers the run's order, downtime and
rollbacks.

## Deliver changed YAML with chalkctl config apply

A change to the documents alone, such as a new `Peer`, needs no upgrade. `--diff` shows what a
delivery changes in the documents. `--dry-run` has the node check the delivery, its renderers
and their checks included, and print the effects it would take. Neither changes anything:

```sh
chalkctl config apply node1 --secrets=secrets.age --diff
chalkctl config apply node1 --secrets=secrets.age --dry-run
chalkctl config apply node1 --secrets=secrets.age
```

The node renders and checks every output before it changes anything, so a document that gives
bird a config `bird -p` rejects refuses the whole delivery. chalkctl prints each unit the effects
restarted, as `restarted <unit>`, and then the config the node runs, as `node1 runs config <hash>`.
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
chalkctl status node1
```

The outputs form a table with three columns:

- `OUTPUT` is the output's name, such as `bird-conf`.
- `HASH` is the start of the hash of the files the node serves, or `none` while it serves none.
- `STATE` is `rendered`, `failed:` with the last failure, or `not rendered yet`.

A node whose store is not the image the inventory names gets a line that starts with
`its store's root hash is`, and that names the command that fixes it. A unit that failed is named
as `failed unit <unit>`.

## chalkctl logs shows a unit's journal

`chalkctl logs` prints a node's journal of the current boot, for one unit with `--unit`:

```sh
chalkctl logs node1 --unit=bird.service
```

A render or a check runs as a transient unit, `chalkos-render-<n>.service`, which keeps the
program's standard error. The journal of `chalkd.service` names that unit beside the failure:

```sh
chalkctl logs node1 --unit=chalkd.service
chalkctl logs node1 --unit=chalkos-render-<n>.service
```

`--follow` keeps printing new entries until you interrupt it.

## What next

- [Upgrade nodes](../operate/upgrade.md) rolls an image through a cluster node by node.
- [The config pipeline](../how-it-works/node/config-pipeline.md) explains what a node does with a
  delivery, at boot and after an upgrade.
- [Troubleshooting](../operate/troubleshooting.md) starts from `chalkctl status` when a node
  misbehaves.
