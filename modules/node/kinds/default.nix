# Config kinds: the documents an image accepts at runtime, declared in chalkos.kinds.
# - Each kind becomes a JSON Schema in /etc/chalkos/schemas and in the image directory's schemas/.
#   chalkctl and chalkd validate a node's config documents against them.
# - Each kind has a get function that builds chalkcfg commands.
# - The schemas include networkd's kinds (networkd.nix).
# The image builds only when chalkos-rules accepts the rules of its kinds, facts (facts.nix),
# outputs and effects.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  schema = import ../../../lib/kinds/schema.nix { inherit lib; };
  getLib = import ../../../lib/kinds/get.nix { inherit lib; };
  api = import ../../../lib/kinds/api.nix;
  inherit (chalkosPackages) chalkcfg chalkos-rules;
  cfg = config.chalkos.kinds;

  # A change rule, of a kind or, in the same shape, of a field.
  ruleModule = {
    options = {
      rule = lib.mkOption {
        type = lib.types.nonEmptyStr;
        example = "self == oldSelf";
        description = "CEL expression that is true for every change the node accepts.";
      };
      message = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Why a change is refused and what to do instead.";
      };
      messageExpression = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = ''"the rack moves from " + oldSelf + " to " + self'';
        description = "CEL expression that yields the message, with the rule's variables; `message` is used when it fails.";
      };
      takes = lib.mkOption {
        type = lib.types.enum [
          "refuse"
          "reset"
          "reinstall"
        ];
        default = "refuse";
        description = "What a change the rule refuses needs: `refuse` cannot be made, `reset` takes `chalkctl storage reset`, `reinstall` a reinstall of the node.";
      };
      description = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "A Peer keeps its AS number.";
        description = "One sentence on what the rule guards, which the reference pages show; the node never reads it.";
      };
    };
  };

  # The files of the image chalkos-rules checks, laid out as it reads them; units holds every
  # systemd unit of the image, the upstream ones among them.
  rulesImage = pkgs.linkFarm "chalkos-rules-image" {
    schemas = schemas;
    "facts.json" = config.system.build.chalkosFacts;
    "effects.json" = config.system.build.chalkosEffects;
    "outputs.json" = config.system.build.chalkosOutputs;
    "examples.json" = config.system.build.chalkosOutputExamples;
    units = config.environment.etc."systemd/system".source;
  };
  # The image builds only when every rule of its schemas and of its effects compiles, within the
  # cost limit, and reads facts the image declares, its effects act on units and outputs it
  # defines, and each output renders its examples and passes its check.
  rulesCheck = pkgs.runCommand "chalkos-rules-check" { nativeBuildInputs = [ chalkos-rules ]; } ''
    chalkos-rules check ${rulesImage}
    touch $out
  '';

  kindModule =
    { name, config, ... }:
    {
      options = {
        group = lib.mkOption {
          type = lib.types.strMatching "[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+";
          example = "bgp.example.org";
          description = "API group of the kind: a DNS name its author controls, as for Kubernetes custom resources.";
        };
        version = lib.mkOption {
          type = lib.types.strMatching "v[0-9]+((alpha|beta)[0-9]+)?";
          example = "v1alpha1";
          description = "API version of the kind. An incompatible change to the spec is a new version.";
        };
        multiple = lib.mkOption {
          type = lib.types.bool;
          default = false;
          description = "Whether a node's config may hold more than one document of the kind.";
        };
        description = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          example = "The rack the node is mounted in.";
          description = "Sentence that says what a document of the kind is for, which the kind's schema carries as its description.";
        };
        builtin = lib.mkOption {
          type = lib.types.bool;
          default = false;
          internal = true;
          description = "Whether chalkos declares the kind itself; only such kinds may use the groups chalkos.dev and *.chalkos.dev.";
        };
        spec = lib.mkOption {
          type = lib.types.attrsOf lib.types.raw;
          default = { };
          example = lib.literalExpression ''
            {
              asn = lib.mkOption { type = lib.types.ints.u32; description = "Local AS number."; };
            }
          '';
          description = ''
            Option declarations of the document's `spec`, as under a module's `options`. They are
            converted into the kind's JSON Schema: an option without a default is required, and
            `description`, `default` and `example` carry over. A field declares change rules with
            `lib.chalkos.mkOption { …; rules = [ { rule = "self == oldSelf"; message = "…"; } ]; }`,
            in the shape of `rules`, where `self` is the field's new value and `oldSelf` its value in
            the running config.
          '';
        };
        rules = lib.mkOption {
          type = lib.types.listOf (lib.types.submodule ruleModule);
          default = [ ];
          example = lib.literalExpression ''
            [
              {
                rule = "size(documents) <= 1";
                message = "a node is in one rack";
              }
            ]
          '';
          description = ''
            Change rules over the node's documents of the kind, CEL expressions that are true for
            every config the node accepts. `documents` and `oldDocuments` map document names to
            the documents of the kind in the delivered config and in the running one; `config`,
            `oldConfig`, `node.name` and `facts` are as for every rule. A rule that is false
            refuses the config, or says it takes a reset or a reinstall.
          '';
        };
        dir = lib.mkOption {
          type = lib.types.str;
          readOnly = true;
          default = "/run/chalkos/config/${config.group}/${name}";
          defaultText = lib.literalExpression ''"/run/chalkos/config/''${group}/<kind>"'';
          description = "Directory on the running node that holds the node's documents of the kind, one JSON file per document.";
        };
        get = lib.mkOption {
          type = lib.types.functionTo lib.types.str;
          readOnly = true;
          default = getLib.command {
            inherit chalkcfg;
            kind = name;
            inherit (config) spec;
          };
          defaultText = lib.literalMD "a function of `{ name ? null, path ? null, output ? null }`";
          example = lib.literalExpression ''
            config.chalkos.kinds.Rack.get { name = "rack"; path = "spec.location"; }
            # "/nix/store/…-chalkcfg-0.1.0/bin/chalkcfg get Rack rack -o 'jsonpath={.spec.location}'"
          '';
          description = ''
            Builds a shell command that prints the node's documents of the kind with chalkcfg.
            `name` picks one document, and must be a document name such as `rack`; without it the
            command prints every document of the kind, one line each. `path` prints one field, such
            as `spec.location`: fields separated by dots, `*` or an index for the elements of a
            list, and a key or `*` for the values of an attribute set. The path must follow the
            kind's options, or the evaluation fails and lists the fields where it went wrong.
            `output` passes `json`, `yaml`, `jsonpath=<expression>` or `go-template=<template>`
            instead of a path. Run the command from a shell script, such as
            `pkgs.writeShellScript`, since systemd expands `$` and `%` in `ExecStart=` itself.

            A path is checked against the kind's options, not against the documents. Where a
            document leaves the field out, or its list is shorter than the index, the command
            exits with status 1; without `name`, one such document fails the whole command. A
            field set to null prints `null`. `*` over an attribute set prints the values in no particular order; for a
            stable order, use an `output` such as
            `go-template={{range $k, $v := .spec.labels}}{{$k}}={{$v}} {{end}}`, which goes
            through the keys sorted. A path cannot address a key that contains `.`, whitespace or
            one of `'`, `\`, `@`, `$`, `,`, `[`, `]`, `{` and `}`; the evaluation fails on such a
            key, and a go-template `output` reads it instead.
          '';
        };
      };
    };

  # Kind names follow Kubernetes: upper camel case.
  badNames = lib.filter (kind: builtins.match "[A-Z][A-Za-z0-9]*" kind == null) (lib.attrNames cfg);
  takenNames = lib.filter (kind: lib.elem kind api.networkdKinds) (lib.attrNames cfg);
  reservedGroups = lib.filter (kind: cfg.${kind}.group == api.groups.networkd) (lib.attrNames cfg);
  chalkosGroups = lib.filter (
    kind:
    !cfg.${kind}.builtin
    && (cfg.${kind}.group == "chalkos.dev" || lib.hasSuffix ".chalkos.dev" cfg.${kind}.group)
  ) (lib.attrNames cfg);
  kinds =
    if badNames != [ ] then
      throw "chalkos.kinds.${lib.head badNames}: a kind's name must match [A-Z][A-Za-z0-9]*, as Kubernetes kinds do"
    else if takenNames != [ ] then
      throw "chalkos.kinds.${lib.head takenNames}: the name is taken by chalkos's networkd kind of the same name"
    else if reservedGroups != [ ] then
      throw "chalkos.kinds.${lib.head reservedGroups}: the group ${api.groups.networkd} is reserved for chalkos's networkd kinds"
    else if chalkosGroups != [ ] then
      throw "chalkos.kinds.${lib.head chalkosGroups}: the groups chalkos.dev and *.chalkos.dev are reserved for chalkos's own kinds"
    else
      lib.mapAttrsToList (kind: k: {
        inherit kind;
        inherit (k)
          group
          version
          multiple
          spec
          rules
          description
          ;
      }) cfg;

  # The schema of each kind of chalkos.kinds, by file name.
  kindSchemas = lib.listToAttrs (
    map (k: lib.nameValuePair (schema.fileName k) (schema.kindSchema k)) kinds
  );
  schemas = pkgs.runCommand "chalkos-schemas" { } ''
    mkdir $out
    cp ${config.system.build.chalkosNetworkdSchemas}/*.json $out/
    ${lib.concatStrings (
      lib.mapAttrsToList (file: s: ''
        cp ${pkgs.writeText file (builtins.toJSON s)} $out/${file}
      '') kindSchemas
    )}
  '';
in
{
  imports = [
    ./builtin
    ./facts.nix
    ./networkd.nix
  ];

  options.chalkos.kinds = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule kindModule);
    default = { };
    description = ''
      Config kinds the image accepts, by kind name. A node's config is a set of documents of
      these kinds; chalkctl and chalkd validate each against the kind's schema.
    '';
  };

  config = {
    system.build.chalkosSchemas = schemas;
    system.build.chalkosKindSchemas = kindSchemas;
    system.build.chalkosRulesCheck = rulesCheck;
    system.build.chalkosRulesImage = rulesImage;
    system.checks = [ rulesCheck ];
    system.build.chalkosKinds = map (k: {
      apiVersion = "${k.group}/${k.version}";
      inherit (k) kind multiple;
    }) (config.system.build.chalkosNetworkdKinds ++ kinds);
    environment.etc."chalkos/schemas".source = schemas;
  };
}
