---
title: "Write your first extension"
description: "Add a config kind, a rendered file, a test and a unit to the lab's image, then change the config on a node"
---

# Write your first extension

This tutorial adds an extension to the image of the quick start's lab. The extension declares a
config kind, `Hardware`, which records who made a node's server and which model it is. The image
renders each node's `Hardware` document into an environment file, and a unit logs its contents.
At the end you have built the kind's schema, checked the nodes' config, tested the extension, run
it on the lab and changed it on a node. It takes about 15 minutes, most of it for the image build
and the lab.

The extension is `examples/extensions/hardware.nix` of the chalkos repository.

## Before you begin

You need:

- the machine the [quick start](../get-started/quick-start-kubernetes.md#before-you-begin) needs;
- a lab directory made from the `lab` template, as the quick start makes it up to
  [Generate the cluster's secrets](../get-started/quick-start-kubernetes.md#generate-the-clusters-secrets):
  under git, with `flake.lock` and `secrets.json`;
- no lab named `lab` that still runs. If the quick start's lab runs, `chalklab destroy` removes it;
- some Nix: NixOS modules and their options.

Run every command of this page in the lab directory, in the shell `nix develop` opens.

## Write the extension

The output renders a file from a template. Create `extensions/hardware.env.tmpl`:

```text title="extensions/hardware.env.tmpl"
--8<-- "examples/extensions/hardware.env.tmpl"
```

The template is Go's `text/template`. It reads the node's `Hardware` documents as
`.documents.Hardware` and writes one variable per field. `quote` writes a value as a
double-quoted string, and `default` gives the empty string for a missing asset tag.

Create `extensions/hardware.nix`, the module that declares the kind, the output and the unit:

```nix title="extensions/hardware.nix"
--8<-- "examples/extensions/hardware.nix:head"
--8<-- "examples/extensions/hardware.nix:tail"
```

The module has four parts:

- `chalkos.kinds.Hardware` declares the kind, in the group `hardware.example.org`. Each option of
  `spec` becomes a field. An option without a default, such as `vendor`, is required.
- `chalkos.outputs.hardware` renders `hardware.env` from the node's `Hardware` documents with the
  template.
- `chalkos.effects.hardware-info` restarts the unit when the output's files change.
- `systemd.services.hardware-info` reads the rendered file as its environment and prints one line.
  The `-` before the path lets the unit start on a node without the file. The unit is a bash
  script, which the image takes only with `chalkos.shell.allow`;
  [Keep an image shell-free](shell-free.md) shows units that need no shell.

Add the module to the image `node-kvm` in `flake.nix`:

```diff
-            modules = [ chalkos.nixosModules.kubernetes ];
+            modules = [
+              chalkos.nixosModules.kubernetes
+              ./extensions/hardware.nix
+            ];
```

Nix reads a flake from the files git tracks, so add the new files to git:

```sh
git add extensions flake.nix
```

## Look at the kind's schema

The image carries a JSON Schema for each kind it accepts. chalkctl and chalkd check every document
against it. Build the schemas without building the image:

```console
$ nix build .#nixosConfigurations.node-kvm.config.system.build.chalkosSchemas
$ ls result
chalkos.dev_v1alpha1_Host.json
chalkos.dev_v1alpha1_Secret.json
hardware.example.org_v1alpha1_Hardware.json
kubernetes.chalkos.dev_v1alpha1_Addon.json
...
time.chalkos.dev_v1alpha1_TimeSync.json
```

`hardware.example.org_v1alpha1_Hardware.json` is the new kind's. Its `spec` holds the three
options:

```console
$ nix run nixpkgs#jq -- .properties.spec result/hardware.example.org_v1alpha1_Hardware.json
{
  "additionalProperties": false,
  "properties": {
    "assetTag": {
      "anyOf": [
        {
          "minLength": 1,
          "type": "string"
        },
        {
          "type": "null"
        }
      ],
      "default": null,
      "description": "Asset tag of the server, if it has one.",
      "examples": [
        "HL-0042"
      ]
    },
    "model": {
      "description": "Model of the node's server.",
      "examples": [
        "SYS-E300-9D"
      ],
      "minLength": 1,
      "type": "string"
    },
    "vendor": {
      "description": "Vendor of the node's server.",
      "examples": [
        "Supermicro"
      ],
      "minLength": 1,
      "type": "string"
    }
  },
  "required": [
    "model",
    "vendor"
  ],
  "type": "object"
}
```

`lib.types.nonEmptyStr` became a string of at least one character. `vendor` and `model` are
required, and `assetTag` may be null. `additionalProperties: false` refuses a field the kind does
not declare.

## Give w1 a Hardware document

Add this document to the end of `nodes/w1.yaml`, after a `---` line:

```yaml
apiVersion: hardware.example.org/v1alpha1
kind: Hardware
metadata: {name: hardware}
spec: {vendor: Supermicro, model: SYS-E300-9D}
```

cp1 gets no `Hardware` document, so its unit finds no file.

## Check the config against the image

The template's inventory names the published image, which lacks the extension. Point it at your
build instead, in `chalkos.yaml`:

```yaml title="chalkos.yaml"
    node-kvm: {source: {directory: images/node-kvm}}
```

Build the image where the inventory now points, and check each node's config against it with
[`chalkctl validate`](../reference/cli/chalkctl_validate.md):

```console
$ nix build .#node-kvm -o images/node-kvm
$ chalkctl validate
cp1: valid for the image node-kvm (node-kvm on kvm, root hash 5425995f5799), config cff86e2957830b9ec4a10a6f6966660695c5a332fca12103d59f79ab4c1ccc15
w1: valid for the image node-kvm (node-kvm on kvm, root hash 5425995f5799), config d8ddf99001390c55c88d34312a8ce7db6eee5f3d08a19f47e4b5fc50f90eaa55
```

Your root hash and config versions differ. To see the schema refuse a mistake, misspell `model` as
`modle` in `nodes/w1.yaml` and run the check again:

```console
$ chalkctl validate
cp1: valid for the image node-kvm (node-kvm on kvm, root hash 5425995f5799), config cff86e2957830b9ec4a10a6f6966660695c5a332fca12103d59f79ab4c1ccc15
chalkctl: the config of 1 node is invalid:
node w1: Hardware "hardware": - at '/spec': additional properties 'modle' not allowed
- at '/spec': missing property 'model'
```

The refusal names the node, the document and the field. Change `modle` back to `model`.

## Test the extension

A test gives the image a config and compares what it renders with golden files: files you checked
once by eye. It runs in the Nix sandbox, without a node.

Create the test's config, `extensions/hardware-examples/server.yaml`:

```yaml title="extensions/hardware-examples/server.yaml"
--8<-- "examples/extensions/hardware-examples/server.yaml"
```

Declare the test in `extensions/hardware.nix`, after `chalkos.outputs.hardware`. It has no golden
files yet:

```nix
chalkos.tests.hardware-server = {
  config = ./hardware-examples/server.yaml;
};
```

Add the file to git and build what each test of the image got:

```console
$ git add extensions
$ nix build .#nixosConfigurations.node-kvm.config.system.build.chalkosTestResults
$ cat result/summary.txt
PASS chrony-empty
PASS chrony-servers
PASS hardware-server
PASS networkd-none
PASS networkd-static
PASS networkd-vlan
$ cat result/hardware-server/outputs/hardware/hardware.env
VENDOR="Supermicro"
MODEL="SYS-E300-9D"
ASSET_TAG="HL-0042"
```

The `chrony` and `networkd` tests are chalkos's own, which every image carries.
`hardware.env` holds what the unit should read, so keep it as the golden files:

```sh
mkdir -p extensions/tests/server
cp -r --no-preserve=mode result/hardware-server/outputs/hardware extensions/tests/server/hardware
git add extensions
```

Name the directory in the test, so every later build compares the rendered file with it:

```nix
--8<-- "examples/extensions/hardware.nix:test"
```

Run the flake's checks, which run the image's tests:

```console
$ nix flake check
evaluating flake...
...
checking flake output 'checks'...
checking derivation checks.x86_64-linux.node-kvm...
derivation evaluated to /nix/store/8qbr0jljylickbwk0h9kmxdzxr3fypzq-chalkos-tests.drv
...
running 3 flake checks...
building '/nix/store/yfskai0apdkv2ikjckxy016gds554p4z-chalkos-tests.json.drv'...
building '/nix/store/g1c3dr2wb0lwf5q4cafm9xxbkvldx2zr-chalkos-test-results.drv'...
building '/nix/store/8qbr0jljylickbwk0h9kmxdzxr3fypzq-chalkos-tests.drv'...
all checks passed!
```

A change to the template that alters `hardware.env` now fails the check, with a diff.

## Run the extension on the lab

The tests change nothing in the image, so `images/node-kvm` is still current. Create the lab with
[`chalklab create`](../reference/cli/chalklab_create.md), as the quick start does:

```console
$ chalklab create --secrets secrets.json
creating the lab's Secure Boot keys in /home/alice/.local/state/chalklab/lab/keys
copying the image of cp1 from /home/alice/lab/images/node-kvm
copying the image of w1 from /home/alice/lab/images/node-kvm
starting cp1, w1
...
cp1 is bootstrapped; applied 19 objects
wrote /home/alice/.local/state/chalklab/lab/kubeconfig; its certificate is valid for 8760h0m0s

the lab lab runs cp1, w1; next:
  export KUBECONFIG=/home/alice/.local/state/chalklab/lab/kubeconfig
  export CHALKOSCONFIG=/home/alice/.local/state/chalklab/lab/chalkctl.json
  export CHALKOSINVENTORY=/home/alice/.local/state/chalklab/lab/chalkos.yaml
...
```

Export the client file and the lab's inventory, as `chalklab create` printed, so chalkctl can
reach the nodes. Then read the unit's journal on each node
with [`chalkctl logs`](../reference/cli/chalkctl_logs.md):

```console
$ export CHALKOSCONFIG=~/.local/state/chalklab/lab/chalkctl.json
$ export CHALKOSINVENTORY=~/.local/state/chalklab/lab/chalkos.yaml
$ chalkctl logs w1 --unit=hardware-info.service
2026-11-16T10:54:36+00:00 w1 systemd[1]: Starting hardware-info.service...
2026-11-16T10:54:36+00:00 w1 9ci6jmda4j998yhh2wvridqpdga1jw23-hardware-info[508]: hardware: Supermicro SYS-E300-9D
2026-11-16T10:54:36+00:00 w1 systemd[1]: Finished hardware-info.service.
$ chalkctl logs cp1 --unit=hardware-info.service
2026-11-16T10:54:17+00:00 cp1 systemd[1]: Starting hardware-info.service...
2026-11-16T10:54:18+00:00 cp1 9ci6jmda4j998yhh2wvridqpdga1jw23-hardware-info[522]: hardware: unknown unknown
2026-11-16T10:54:18+00:00 cp1 systemd[1]: Finished hardware-info.service.
```

w1 logs its document. cp1 has none, so its `hardware.env` is empty and the unit prints its
fallback.

## Change the document on w1

Give w1's server an asset tag. Change the document's `spec` in `nodes/w1.yaml`:

```yaml
apiVersion: hardware.example.org/v1alpha1
kind: Hardware
metadata: {name: hardware}
spec: {vendor: Supermicro, model: SYS-E300-9D, assetTag: LAB-0001}
```

Check the config, then deliver it to w1 with
[`chalkctl config apply`](../reference/cli/chalkctl_config_apply.md). Delivering config needs the secrets
file:

```console
$ chalkctl validate
cp1: valid for the image node-kvm (node-kvm on kvm, root hash 5425995f5799), config cff86e2957830b9ec4a10a6f6966660695c5a332fca12103d59f79ab4c1ccc15
w1: valid for the image node-kvm (node-kvm on kvm, root hash 5425995f5799), config 9307d1da8a3a17a8e611ded2cdbdd577692950cc6eb963ce1712ecf656a25416
$ chalkctl config apply w1 --secrets secrets.json
w1: config 0 added, 0 removed, 1 changed
restarted hardware-info.service
w1 runs config 9307d1da8a3a17a8e611ded2cdbdd577692950cc6eb963ce1712ecf656a25416
```

The node rendered a new `hardware.env`, and the effect restarted the unit. No new image was
needed. The unit's journal shows the new line:

```console
$ chalkctl logs w1 --unit=hardware-info.service
...
2026-11-16T10:55:02+00:00 w1 systemd[1]: Starting hardware-info.service...
2026-11-16T10:55:02+00:00 w1 9ci6jmda4j998yhh2wvridqpdga1jw23-hardware-info[1173]: hardware: Supermicro SYS-E300-9D, asset tag LAB-0001
2026-11-16T10:55:02+00:00 w1 systemd[1]: Finished hardware-info.service.
```

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) lists the new output among the node's outputs, rendered:

```console
$ chalkctl status w1
config 9307d1da8a3a17a8e611ded2cdbdd577692950cc6eb963ce1712ecf656a25416 (the inventory's)
previous config d8ddf99001390c55c88d34312a8ce7db6eee5f3d08a19f47e4b5fc50f90eaa55 (config rollback restores it)
platform kvm (the inventory's)
image 5425995f5799, booted from chalkos_1.efi
VOLUME  DISK    MOUNT POINT  STATE
var     system  /var         mounted
OUTPUT    HASH          STATE
chrony    0af9b0bd41ca  rendered
hardware  e81d9a3cbe14  rendered
networkd  195ac3c4ec51  rendered
...
```

Remove the lab with [`chalklab destroy`](../reference/cli/chalklab_destroy.md) when you are done:

```console
$ chalklab destroy
removed the lab in /home/alice/.local/state/chalklab/lab
```

## What next

- [Write a BGP extension](../guides/bgp-extension.md) is the fuller tutorial: two kinds, rules, a
  validator, secrets and a diagnostic.
- [How an extension fits together](index.md#how-an-extension-fits-together) shows where each part
  runs and which parts can refuse a delivery.
- [Develop and debug an extension](develop-an-extension.md) runs the checks from the fastest
  build to a node and lists the common errors.
- [Declare a config kind](declare-a-kind.md) starts the how-to pages, one task each.
- The [extension API](../reference/extension-api.md) lists every option and `chalkosLib` helper.
