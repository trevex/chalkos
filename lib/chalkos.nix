# chalkosLib, the module argument every image's NixOS modules get, and the flake's lib besides
# mkImage and mkImages: the types of extension kinds' fields, the renderers of chalkos.outputs, the validators
# of chalkos.validators and the common effects. It holds no store paths, since lib does not depend
# on pkgs: a renderer is a value that chalkos.outputs.<name>.render turns into its program's
# command, as a validator is for chalkos.validators.<name>.program.
{ lib }:
let
  kindTypes = import ./kinds/types.nix { inherit lib; };
  api = import ./kinds/api.nix;
  # The kinds an image declares: the attributes of its chalkos.kinds, given as kinds, and
  # networkd's.
  declaredKinds = kinds: lib.attrNames kinds ++ api.networkdKinds;
  refuseLib = import ./refuse.nix { inherit lib; };
  kindCheck = import ./kinds/check.nix {
    inherit lib declaredKinds;
    inherit (refuseLib) refuse;
  };
  # An effect that acts on units when one of outputs or the documents of one of kinds changed.
  effectOn =
    action:
    {
      outputs ? [ ],
      kinds ? [ ],
    }:
    units: {
      inherit outputs kinds;
      ${action} = units;
    };
in
{
  types = {
    inherit (kindTypes)
      ref
      secretRef
      address
      prefix
      portRange
      imageReference
      ;
  };

  # lib.mkOption, for a field of a kind, with the field's change rules: each { rule; message or
  # messageExpression; takes ? "refuse"; description ? null; }, as a kind's rules are; and the
  # values that must not repeat: true, or one or a list of { across ? "kind" or "list"; by ? [
  # field or { field; as ? null; } ]; as ? null; hint ? null; }; and maxItems, the most items a
  # list holds, which bounds what a rule that loops over the list costs. The schema converter
  # checks unique and maxItems against the field's type.
  mkOption =
    {
      rules ? [ ],
      unique ? false,
      maxItems ? null,
      ...
    }@args:
    lib.mkOption (
      removeAttrs args [
        "rules"
        "unique"
        "maxItems"
      ]
    )
    // lib.optionalAttrs (rules != [ ]) { "x-chalkos-rules" = rules; }
    // lib.optionalAttrs (unique != false) { "x-chalkos-unique" = unique; }
    // lib.optionalAttrs (maxItems != null) { "x-chalkos-maxItems" = maxItems; };

  render = {
    # A Go text/template, which writes the file named as the template less .tmpl; a missing or
    # null key prints as nothing.
    template = path: { template = path; };
    # A jq program, whose output, raw for a string, writes the file named as the program less .jq.
    jq = path: { jq = path; };
    # The documents of kinds as they are, each as <Kind>/<name>.json.
    documents = kinds: { documents = kinds; };
  };

  validate = {
    # A jq program that reads the input of the documents by kind and makes the report of refusals.
    jq = path: { jq = path; };
  };

  restartOn = effectOn "restart";
  reloadOn = effectOn "reload";

  # What chalkos's own modules share, which extensions do not call.
  internal =
    refuseLib
    // kindCheck
    // {
      inherit api kindTypes declaredKinds;
      # The version of secrets.pub.json chalkos reads.
      secretsVersion = import ./secrets-version.nix;
      cel = import ./cel.nix;
      schema = import ./kinds/schema.nix { inherit lib; };
      get = import ./kinds/get.nix { inherit lib; };
      # The fields that make a kind of the group a built-in one.
      builtin = group: {
        inherit group;
        inherit (api) version;
        builtin = true;
      };
      # The option <name>.enable of a feature that images of a layer may leave out, true unless an
      # image sets it false. Its description says that the layer's images carry `carries` and
      # accept the kinds named; layer is the layer as the description names it, such as router.
      feature =
        layer: name:
        {
          carries,
          kinds,
        }:
        let
          names = map (k: "`${k}`") kinds;
          kindsText =
            if lib.length names == 1 then
              "the kind ${lib.head names}"
            else
              "the kinds ${lib.concatStringsSep ", " (lib.init names)} and ${lib.last names}";
        in
        {
          ${name}.enable = lib.mkOption {
            type = lib.types.bool;
            default = true;
            description =
              "Whether ${layer} images carry ${carries} and accept ${kindsText}. Only with the ${layer} "
              + "layer; set it false in an image's modules to leave the feature out of it.";
          };
        };
      # The sandbox of a root unit that needs no capability and no network: Unix sockets alone,
      # which stay reachable on a read-only system and from the private network namespace, as it
      # hides abstract sockets alone; no home; kernel tunables, modules and cgroups out of reach;
      # and the system calls of a system service of the native architecture.
      unprivileged = {
        CapabilityBoundingSet = "";
        NoNewPrivileges = true;
        PrivateNetwork = true;
        RestrictAddressFamilies = [ "AF_UNIX" ];
        ProtectSystem = "strict";
        ProtectHome = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        SystemCallFilter = [ "@system-service" ];
        MemoryDenyWriteExecute = true;
        LockPersonality = true;
        RestrictRealtime = true;
        SystemCallArchitectures = "native";
      };
      # The type of a program option: a command, a package, whose path is the command, or an
      # attribute set of a builder, such as chalkosLib.render's, which the option's module checks.
      programType =
        builders:
        lib.types.either (lib.types.coercedTo lib.types.package toString lib.types.nonEmptyStr) (
          lib.types.attrsOf lib.types.raw
        )
        // {
          description = "command, package or ${builders} value";
        };
      # A DNS name: letters, digits, dots and dashes, starting and ending with a letter or digit.
      dnsName = "[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?";
      # The config files of the directory dir, YAML or JSON, as tests of chalkos.tests named
      # <prefix>-<the file's name without its extension>, each with the file as its config alone:
      # a test that the image accepts the config, renders every output from it and passes the
      # outputs' checks. Two files that give one name, such as a.yaml and a.json, are refused.
      exampleTests =
        prefix: dir:
        let
          # The name of the file's test, or null for a file that is no config file.
          testName =
            file:
            let
              m = builtins.match "(.*)\\.(yaml|yml|json)" file;
            in
            if m == null then null else "${prefix}-${lib.head m}";
          files = lib.attrNames (
            lib.filterAttrs (file: type: type == "regular" && testName file != null) (builtins.readDir dir)
          );
          byName = lib.groupBy testName files;
          clashes = lib.filterAttrs (_: fs: lib.length fs > 1) byName;
        in
        refuseLib.refuse (lib.mapAttrsToList (
          name: fs: "${toString dir}: ${lib.concatStringsSep " and " fs} both give the test ${name}"
        ) clashes) (lib.mapAttrs (_: fs: { config = dir + "/${lib.head fs}"; }) byName);
    };
}
