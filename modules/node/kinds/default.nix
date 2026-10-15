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
            `description`, `default` and `example` carry over.
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
            `name` picks one document; without it the command prints every document of the kind,
            one line each. `path` prints one field, such as `spec.location`: fields separated by
            dots, `*` or an index for the elements of a list, and a key or `*` for the values of an
            attribute set. The path must follow the kind's options, or the evaluation fails and
            lists the fields where it went wrong. `output` passes `json`, `yaml`,
            `jsonpath=<expression>` or `go-template=<template>` instead of a path. Run the command
            from a shell script, such as `pkgs.writeShellScript`, since systemd expands `$` and `%`
            in `ExecStart=` itself.
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
          ;
      }) cfg;

  schemas = pkgs.runCommand "chalkos-schemas" { } ''
    mkdir $out
    cp ${networkdSchemas}/*.json $out/
    ${lib.concatMapStrings (k: ''
      cp ${pkgs.writeText (schema.fileName k) (builtins.toJSON (schema.kindSchema k))} $out/${schema.fileName k}
    '') kinds}
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
    };
  };

  config = {
    _module.args.chalkosLib.types = { inherit (schema.types) secretRef; };

    system.build.chalkosSchemas = schemas;
    system.build.chalkosKinds = map (k: {
      apiVersion = "${k.group}/${k.version}";
      inherit (k) kind multiple;
    }) (map networkdKind networkd.kinds ++ kinds);
    environment.etc."chalkos/schemas".source = schemas;
  };
}
