---
title: "Layer plugins"
description: "The Go plugins of a layer in chalkd, chalkctl, the console and the status view, and the hooks each one implements"
---

# Layer plugins

[An image carries the base and at most one layer](../../how-it-works/node/architecture.md#an-image-carries-the-base-and-at-most-one-layer) explains layers; this page maps a layer's Go side to the code.

A layer's Nix side is a predefined set of NixOS modules, `chalkos.nixosModules.kubernetes` or
`chalkos.nixosModules.router`. An image lists it in its modules like any other module, and it
enables the layer's modules in `modules/node`. Its Go side is a plugin compiled into chalkd and
chalkctl, plus a console tab and a status section. The image names its layers with markers in
`/etc/chalkos/layers`, and a node's Info names them.

## chalkd's plugins

`pkg/chalkd/layer` declares chalkd's side. A `Plugin` has a name, its API's handler, the
permissions of its methods and the background loops it starts in normal mode. Optional hooks add
what else the layer does. chalkd finds them by type assertion.

| Hook | What the layer adds |
| --- | --- |
| `Health` | Its check of a new boot's health. |
| `Certificates` | Its certificates and trusted CAs in Status. |
| `Delivery` | The payloads it takes with a delivery and its part of the identity. |
| `nodeconfig.Layer` | What it writes from the running slot's documents, at boot and after each delivery. |
| `Issuer` | The issuing of node certificates on renewal; an image has one issuer at most. |
| `ImageFacts` | The facts of its images, from which it adds its translator to the target that config is translated for. |
| `Commands` | Cobra subcommands, run as `chalkd <layer> <command>`. |
| `Actions` | Actions on what the running chalkd holds, which effects name as `<layer>:<action>`. |

`cmd/chalkd/plugins.go` lists the compiled plugins. `layer.Registry` sorts them into the compiled
and the active ones:

- chalkd mounts the API of every compiled plugin, so the authoriser can answer for a missing
  layer. It also runs the commands of every compiled plugin.
- It starts only the active plugins, whose layers the image carries, and asks them for most hooks.
- For what may belong to another image, it asks every compiled plugin: the facts of the image an
  upgrade or an install writes, the identity an upgrade's slot keeps, and the payloads the
  installer takes.

The comment of each hook in `pkg/chalkd/layer/layer.go` names the plugins chalkd asks. The plugins
live in `pkg/chalkd/layers/kubernetes` and `pkg/chalkd/layers/router`.

## chalkctl's plugins

`pkg/chalkctl/layer` declares chalkctl's side in the same way. A `Plugin` has hooks for:

- commands;
- a node's status section;
- image facts;
- the payloads of a delivery and their admission;
- the coordinators that upgrade and rotate take first;
- an upgrade's checks;
- the rotation kinds a layer runs;
- the validators it links.

`Inventory` filters the documents each node of an inventory gets and checks the inventory's nodes
together. chalkctl passes the filters to `inventory.Load` and runs the checks on every inventory
it loads. `UpgradeFlags` adds a layer's flags to `chalkctl upgrade` and `chalkctl node reboot`,
such as the Kubernetes plugin's `--allow-downtime`. The layer's hooks of the reboot step read them
back. A plugin also names the nodes every rotation leaves out and those that hold the node CA.

chalkctl runs the rotations of the OS CA and the node CA itself, in `pkg/chalkctl/rotate_ca.go`,
since every node takes part in them. It asks the plugins for their kinds.

Which plugins chalkctl asks depends on where the answer comes from:

- For what the inventory and the secrets file drive, such as commands, payloads and rotations, it
  asks every compiled plugin.
- It asks each compiled plugin for the validators that an image of the node's schemas runs.
- For what it reads from a node's layer services, such as the node's status section and its part
  in an upgrade, it asks only the plugins of the layers the node's Info names.

A plugin reaches the cluster and its nodes through the `Host` chalkctl gives it.
`pkg/chalkctl/layers.go` lists the plugins, which live in `pkg/chalkctl/layers/kubernetes` and
`pkg/chalkctl/layers/router`.

## The console's and the status view's plugins

A layer's section of a node's status is written once, in `pkg/statusview/layers/<layer>`.
chalkctl's plugin prints it for `chalkctl status`, and the console's tab shows it. The console's
side is a `console.Tab` in `pkg/console/layers/<layer>`, which shows only when the node's Info
passes its `Applies`. `cmd/chalkos-console/layers.go` lists the tabs compiled into the console.
