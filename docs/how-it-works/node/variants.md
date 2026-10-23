---
title: "Extensions"
description: "How an extension adds a config kind and the services that read its documents to a variant's images"
---

# Extensions

This page explains how an extension adds a config kind to a variant's images. How `mkImages`
builds variants and platforms is on [Variants and platforms](../../build/variants-and-platforms.md).

## How does an extension add a kind?

An extension is a NixOS module in a variant that declares a config
kind and the services that read its documents. chalkos's own
kinds are declared the same way. The homelab's rack extension records where each node is mounted
and hands the value to a unit on that node:

```nix title="extensions/rack.nix"
# The Rack kind, which records a node's rack position, and a unit that reads it on the node. A
# variant that carries this module accepts Rack documents. A node that was surveyed, whose
# rack-survey tool wrote where it found the node, takes no other location than that once it runs;
# an install reads no facts and takes any.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  chalkos.kinds.Rack = {
    group = "rack.example.org";
    version = "v1alpha1";
    spec.location = lib.chalkos.mkOption {
      type = lib.types.nonEmptyStr;
      example = "rack-a/u12";
      description = "Rack and height unit the node is mounted in.";
      rules = [
        {
          rule = "facts.rackSurvey == null || self == facts.rackSurvey.location";
          messageExpression = ''"the survey found the node at " + facts.rackSurvey.location + ", not at " + self + "; fix the location, or survey the node again"'';
        }
      ];
    };
  };
  # What the rack survey found, {"location": "rack-a/u12"}; null on a node not surveyed yet.
  chalkos.facts.rackSurvey.file = "/var/lib/rack-survey/location.json";
  # The unit reads the Rack when it starts, so a changed Rack restarts it.
  chalkos.effects.rack-location = lib.chalkos.restartOn { kinds = [ "Rack" ]; } [
    "rack-location.service"
  ];
  systemd.services.rack-location = {
    # chalkd writes the node's Rack below /run/chalkos/config for it.
    chalkos.kinds = [ "Rack" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      # A path the kind lacks fails the image's evaluation.
      ExecStart = pkgs.writeShellScript "rack-location" (
        config.chalkos.kinds.Rack.get {
          name = "rack";
          path = "spec.location";
        }
      );
    };
  };
}
```

The module does five things:

- [`chalkos.kinds.Rack`](../../reference/options.md#chalkoskinds) declares the kind: its group,
  version and the options of its `spec`. The image turns the options into a JSON Schema, which
  it carries in `/etc/chalkos/schemas` and its image directory in `schemas/`, so chalkctl and
  chalkd accept `Rack` documents and refuse a misspelled field. A kind's group is a DNS name
  outside `chalkos.dev`, which chalkos keeps for its own kinds, and only option types the schema
  converter knows are allowed; another type fails the build, naming the option.
- The `rules` of `lib.chalkos.mkOption` declare the field's [change rules](rules.md). This one
  reads the [fact](rules.md#what-is-a-fact) `rackSurvey`, which `chalkos.facts` declares, so a
  surveyed node refuses a location other than the one the survey found.
- [`systemd.services.<unit>.chalkos.kinds`](../../reference/options.md#systemdserviceschalkoskinds)
  grants the unit the kind, so chalkd writes its documents for the unit to read.
- [`chalkos.effects`](../../reference/options.md#chalkoseffects) restarts the unit when the node's
  `Rack` changes, because the unit reads it only when it starts, as
  [Outputs and effects](outputs-and-effects.md#what-is-an-effect) describes.
- [`chalkos.kinds.<Kind>.get`](../../reference/options.md#chalkoskindsget) builds the
  chalkcfg command that prints the value. The evaluation
  checks the path `spec.location` against the kind's options, so a typo fails the build instead of
  printing nothing on the node.

Each node's value is a `Rack` document in its YAML files; [Config and the
inventory](config.md#what-is-a-config-document) shows one, and
[How do secrets reach a service?](config.md#how-do-secrets-reach-a-service) covers fields that
reference a `Secret`. Only images of a variant that carries the module accept `Rack` documents,
so a node of another variant refuses a config that holds one.

An extension whose service reads a file rather than documents renders an
[output](outputs-and-effects.md#what-is-an-output). The homelab's hardware extension renders each
node's `Hardware` document through a template into `hardware.env`, which its unit reads as an
environment file, and restarts the unit when the file changes:

```nix title="extensions/hardware.nix"
# The Hardware kind, which records who made a node's server and which model it is, and a unit
# that logs it. The documents reach the unit as an output: an environment file rendered from a
# template, which the build renders from the examples in hardware-examples. A variant that
# carries this module accepts Hardware documents.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  chalkos.kinds.Hardware = {
    group = "hardware.example.org";
    version = "v1alpha1";
    spec = {
      vendor = lib.mkOption {
        type = lib.types.nonEmptyStr;
        example = "Supermicro";
        description = "Vendor of the node's server.";
      };
      model = lib.mkOption {
        type = lib.types.nonEmptyStr;
        example = "SYS-E300-9D";
        description = "Model of the node's server.";
      };
      assetTag = lib.mkOption {
        type = lib.types.nullOr lib.types.nonEmptyStr;
        default = null;
        example = "HL-0042";
        description = "Asset tag of the server, if it has one.";
      };
    };
  };
  # hardware.env, with VENDOR, MODEL and ASSET_TAG; empty on a node without a Hardware document.
  chalkos.outputs.hardware = {
    inputs = [ "Hardware" ];
    render = lib.chalkos.render.template ./hardware.env.tmpl;
    examples = ./hardware-examples;
  };
  # The unit reads the file when it starts, so a changed file restarts it.
  chalkos.effects.hardware-info = lib.chalkos.restartOn { outputs = [ "hardware" ]; } [
    "hardware-info.service"
  ];
  systemd.services.hardware-info = {
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      # Active after it ran, so chalkd's try-restart runs it again.
      RemainAfterExit = true;
      EnvironmentFile = "-${config.chalkos.outputs.hardware.dir}/hardware.env";
      ExecStart = pkgs.writeShellScript "hardware-info" ''
        echo "hardware: ''${VENDOR:-unknown} ''${MODEL:-unknown}''${ASSET_TAG:+, asset tag $ASSET_TAG}"
      '';
    };
  };
}
```

```text title="extensions/hardware.env.tmpl"
{{- range .documents.Hardware -}}
VENDOR={{ quote .spec.vendor }}
MODEL={{ quote .spec.model }}
ASSET_TAG={{ quote (default "" .spec.assetTag) }}
{{ end -}}
```

The build renders the output from the documents in `hardware-examples`, so a template that fails
on them fails the image's build.

An extension's own image options live under a namespace of its own, such as `rack.*` or
`chalkos.rack.*`. Under `chalkos`, the names chalkos declares are taken: `cni`, `debug`,
`disk`, `effects`, `facts`, `kernel`, `kinds`, `kubernetes`, `maintenance`, `outputs`,
`platform`, `provenance`, `upgrade` and `variant`.

## Related pages

- [Config and the inventory](config.md) for the YAML documents nodes receive.
- [Variants and platforms](../../build/variants-and-platforms.md) for the variants that carry an
  extension.
- [mkImages and image options](../../reference/options.md) for every argument and option.
