---
title: "Test and roll out an image"
description: "Check an image and the nodes' config, upgrade the nodes, and confirm their outputs and services"
---

# Test and roll out an image

This page is for an image author whose changed image is ready to reach running nodes. At the end
the image has passed its build checks, every node's config has passed `chalkctl validate`, the
nodes run the image, and `chalkctl status` and `chalkctl logs` show that the outputs rendered and
the services read them. The page uses the homelab and its BGP extension, `extensions/bgp.nix`.

A change reaches a node in one of two ways. A changed image reaches it with
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md), which brings the node's config along.
Changed YAML alone reaches it with
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md), and needs no new image.

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
- Every effect acts on units and outputs that the image defines.
- Every output renders each of its `examples` and passes its `check`. `bird-conf` renders
  `bgp-examples/two-peers.yaml`, and `bird -p` parses the result.
- The store, its hash tree and the UKIs fit their partitions with room to spare, as
  [Add software, drivers and kernel modules](add-software.md#keep-the-image-within-its-slot)
  describes.

The error names the rule, the effect or the output and its example file.
[Render config files](render-config.md#examples-are-rendered-when-the-image-is-built) explains
which examples to write, so that the build covers the documents nodes hold.

## chalkctl validate checks each node's config

`chalkctl validate` checks each node's documents against the schemas of the image the inventory
names for it. It also runs the image's rules that read no facts, and reaches no node:

```console
$ chalkctl validate
cp1: valid for the image node-metal (node on metal, root hash d871203debfe), config 675850c48eaffe4a755dca41652dbf65a5b7959f1e70a3cdd2a94799136fd200
w1: valid for the image node-metal (node on metal, root hash d871203debfe), config 82ec5bbabd10ec79bf76ad892a0bcd7627c9db2ed4c9e252ad0657313359254c
```

Each line names the image, its variant and platform, the start of its root hash, and the hash of
the config the node would get. A `Peer` with `asn: 0`, below the kind's minimum of 1, fails on
every node that gets the file:

```console
$ chalkctl validate
chalkctl: the config of 2 nodes is invalid:
node cp1: Peer "router-b": - at '/spec/asn': minimum: got 0, want 1
node w1: Peer "router-b": - at '/spec/asn': minimum: got 0, want 1
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
node's config changes, such as `cp1: config unchanged`.

A new image's renderers live in its own store, so its outputs render at its first boot. For each
node, chalkctl says so in a line that ends with `renders and checks any outputs it declares when
it first boots; if one fails, the node falls back to the image it runs`. An output that fails then
fails the boot's health check, and the node falls back to the image it ran. The run stops at that
node and shows what the failed boot logged.

[Upgrade nodes](../operate/upgrade.md) covers the run's order, downtime and
rollbacks.

## Deliver changed YAML with chalkctl apply-config

A change to the documents alone, such as a new `Peer`, needs no upgrade. `--diff` shows what a
delivery changes and changes nothing:

```sh
chalkctl apply-config cp1 --secrets=secrets.age --diff
chalkctl apply-config cp1 --secrets=secrets.age
```

The node renders and checks every output before it changes anything, so a document that gives
bird a config `bird -p` rejects refuses the whole delivery. chalkctl prints each unit the effects
restarted, as `restarted <unit>`, and then the config the node runs, as `cp1 runs config <hash>`.
Reloaded units are not listed.

After an upgrade with `--no-reboot`, the staged image keeps a config of its own.
`chalkctl apply-config --staged` delivers to that image instead of the running one.

## chalkctl status shows the outputs

`chalkctl status` shows the image a node booted and whether the boot was found healthy. It also
lists each output the node serves:

```sh
chalkctl status cp1
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
chalkctl logs cp1 --unit=bird.service
```

A render or a check runs as a transient unit, `chalkos-render-<n>.service`, which keeps the
program's standard error. The journal of `chalkd.service` names that unit beside the failure:

```sh
chalkctl logs cp1 --unit=chalkd.service
chalkctl logs cp1 --unit=chalkos-render-<n>.service
```

`--follow` keeps printing new entries until you interrupt it.

## What next

- [Upgrade nodes](../operate/upgrade.md) rolls an image through a cluster node by node.
- [The config pipeline](../how-it-works/node/config-pipeline.md) explains what a node does with a
  delivery, at boot and after an upgrade.
- [Troubleshooting](../operate/troubleshooting.md) starts from `chalkctl status` when a node
  misbehaves.
