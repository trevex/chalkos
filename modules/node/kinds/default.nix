# Config kinds: the documents an image accepts at runtime. Each kind of chalkos.kinds becomes a
# JSON Schema in /etc/chalkos/schemas and in the image directory's schemas/, from which chalkctl
# and chalkd validate a node's config documents. networkd's Network, NetDev and Link come from the
# image's systemd. Each kind of chalkos.kinds has a get function that builds chalkcfg commands;
# networkd's kinds have none, because their schemas exist only once systemd's tables are built and
# a section of theirs may be an object or a list, so a path through them cannot be checked during
# evaluation.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  schema = import ./schema.nix { inherit lib; };
  getLib = import ./get.nix { inherit lib; };
  chalkcfg = pkgs.callPackage ../../../nix/chalkcfg.nix { };
  chalkos-rules = pkgs.callPackage ../../../nix/chalkos-rules.nix { };
  cfg = config.chalkos.kinds;

  # networkd's Network, NetDev and Link, generated from the tables of the systemd the image runs,
  # so their schemas accept exactly the sections and keys its networkd does.
  networkd = {
    group = "networkd.chalkos.dev";
    version = "v1alpha1";
    kinds = [
      "Link"
      "NetDev"
      "Network"
    ];
  };
  networkdKind = kind: {
    inherit kind;
    inherit (networkd) group version;
    multiple = true;
  };
  networkdSchemas = pkgs.stdenvNoCC.mkDerivation {
    name = "chalkos-networkd-schemas";
    inherit (config.systemd.package) src;
    nativeBuildInputs = [
      (pkgs.callPackage ../../../nix/chalkos-networkd-schema.nix { })
      pkgs.jq
    ];
    dontConfigure = true;
    buildPhase = ''
      runHook preBuild
      chalkos-networkd-schema \
        src/network/networkd-network-gperf.gperf \
        src/network/netdev/netdev-gperf.gperf \
        src/udev/net/link-config-gperf.gperf \
        specs
      mkdir $out
      ${lib.concatMapStrings (
        kind:
        let
          k = networkdKind kind;
          envelope = pkgs.writeText "${kind}-envelope.json" (
            builtins.toJSON (schema.envelope (k // { spec = { }; }))
          );
        in
        ''
          jq --slurpfile s specs/${lib.toLower kind}.json \
            '.properties.spec = $s[0].spec | ."$defs" = $s[0]."$defs"' \
            ${envelope} > $out/${schema.fileName k}
        ''
      ) networkd.kinds}
      runHook postBuild
    '';
    dontInstall = true;
    dontFixup = true;
  };

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
    };
  };

  # A fact: node state that rules read as facts.<name>.
  factModule = {
    options = {
      file = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "/state/kubernetes/share.json";
        description = "File whose JSON content is the fact: null while the file is missing. A file that cannot be read, or holds no JSON, refuses the delivery.";
      };
      fields = lib.mkOption {
        type = lib.types.listOf lib.types.nonEmptyStr;
        default = [ ];
        example = [ "kind" ];
        description = "Keys of the file's JSON object the fact keeps, so rules and their messages see nothing else of the file; empty keeps the whole content.";
      };
      command = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = lib.literalExpression ''"''${pkgs.my-tool}/bin/my-tool facts"'';
        description = ''
          Program of the image's store, with its arguments, that prints the fact as JSON. It reads
          `{"config": …, "oldConfig": …}` and its inputs as JSON on standard input, runs through
          `systemd-run` with the system read-only and no network, and must finish within 30
          seconds with at most 1 MiB of output.
        '';
      };
      inputs = lib.mkOption {
        type = lib.types.listOf (lib.types.enum [ "fallbackSecret" ]);
        default = [ ];
        description = "Delivery inputs the command receives besides the config: `fallbackSecret`, the fallback secret delivered with it.";
      };
    };
  };
  factsCfg = config.chalkos.facts;
  # A fact's name is read as facts.<name> in CEL, so it is an identifier.
  badFacts = lib.filter (name: builtins.match "[a-z][A-Za-z0-9]*" name == null) (
    lib.attrNames factsCfg
  );
  # A fact is a file or a command; only a command takes inputs, and only a file keeps fields.
  unsourcedFacts = lib.filter (
    name: (factsCfg.${name}.file == null) == (factsCfg.${name}.command == null)
  ) (lib.attrNames factsCfg);
  fileInputs = lib.filter (name: factsCfg.${name}.file != null && factsCfg.${name}.inputs != [ ]) (
    lib.attrNames factsCfg
  );
  commandFields = lib.filter (
    name: factsCfg.${name}.command != null && factsCfg.${name}.fields != [ ]
  ) (lib.attrNames factsCfg);
  facts =
    if badFacts != [ ] then
      throw "chalkos.facts.${lib.head badFacts}: a fact's name must match [a-z][A-Za-z0-9]*, since rules read it as facts.<name>"
    else if unsourcedFacts != [ ] then
      throw "chalkos.facts.${lib.head unsourcedFacts}: a fact has either a file or a command"
    else if fileInputs != [ ] then
      throw "chalkos.facts.${lib.head fileInputs}: only a command fact takes inputs"
    else if commandFields != [ ] then
      throw "chalkos.facts.${lib.head commandFields}: only a file fact keeps fields"
    else
      lib.mapAttrs (
        _: f:
        if f.file != null then
          { inherit (f) file; } // lib.optionalAttrs (f.fields != [ ]) { inherit (f) fields; }
        else
          { inherit (f) command; } // lib.optionalAttrs (f.inputs != [ ]) { inherit (f) inputs; }
      ) factsCfg;
  factsFile = pkgs.writeText "chalkos-facts.json" (builtins.toJSON facts);
  # The image builds only when every rule of its schemas compiles, within the cost limit, and
  # reads facts the image declares.
  rulesCheck = pkgs.runCommand "chalkos-rules-check" { nativeBuildInputs = [ chalkos-rules ]; } ''
    chalkos-rules check ${schemas} ${factsFile}
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
            `mkOption { … } // { "x-chalkos-rules" = [ { rule = "self == oldSelf"; message = "…"; } ]; }`,
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
            `oldConfig` and `facts` are as for every rule. A rule that is false refuses the
            config, or says it takes a reset or a reinstall.
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
  takenNames = lib.filter (kind: lib.elem kind networkd.kinds) (lib.attrNames cfg);
  reservedGroups = lib.filter (kind: cfg.${kind}.group == networkd.group) (lib.attrNames cfg);
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
      throw "chalkos.kinds.${lib.head reservedGroups}: the group ${networkd.group} is reserved for chalkos's networkd kinds"
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
          ;
      }) cfg;

  # The schema of each kind of chalkos.kinds, by file name.
  kindSchemas = lib.listToAttrs (
    map (k: lib.nameValuePair (schema.fileName k) (schema.kindSchema k)) kinds
  );
  schemas = pkgs.runCommand "chalkos-schemas" { } ''
    mkdir $out
    cp ${networkdSchemas}/*.json $out/
    ${lib.concatStrings (
      lib.mapAttrsToList (file: s: ''
        cp ${pkgs.writeText file (builtins.toJSON s)} $out/${file}
      '') kindSchemas
    )}
  '';
in
{
  imports = [ ./builtin.nix ];

  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.kinds = lib.mkOption {
        type = lib.types.attrsOf (lib.types.submodule kindModule);
        default = { };
        description = ''
          Config kinds the image accepts, by kind name. A node's config is a set of documents of
          these kinds; chalkctl and chalkd validate each against the kind's schema.
        '';
      };
      options.facts = lib.mkOption {
        type = lib.types.attrsOf (lib.types.submodule factModule);
        default = { };
        description = ''
          Facts about the node that change rules read as `facts.<name>`, each from a file or a
          command. chalkd loads a fact once per delivery, when a rule first reads it; chalkctl
          runs no rule that reads one.
        '';
      };
    };
  };

  config = {
    _module.args.chalkosLib.types = { inherit (schema.types) secretRef; };

    system.build.chalkosSchemas = schemas;
    system.build.chalkosKindSchemas = kindSchemas;
    system.build.chalkosFacts = factsFile;
    system.build.chalkosFactDefinitions = facts;
    system.build.chalkosRulesCheck = rulesCheck;
    system.checks = [ rulesCheck ];
    system.build.chalkosKinds = map (k: {
      apiVersion = "${k.group}/${k.version}";
      inherit (k) kind multiple;
    }) (map networkdKind networkd.kinds ++ kinds);
    environment.etc."chalkos/schemas".source = schemas;
    environment.etc."chalkos/facts.json".source = factsFile;
  };
}
