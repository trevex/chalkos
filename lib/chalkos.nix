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
  # A unit's mount namespace in which no file system but the store lets a file run as a program.
  noExec = {
    NoExecPaths = [ "/" ];
    ExecPaths = [ "/nix/store" ];
  };
  # The sandbox of a service: no capabilities, Unix sockets alone, no new privileges, a read-only
  # system, no devices, no home and a private /tmp, kernel settings, modules, logs, cgroups, clock
  # and host name out of reach, no namespaces, realtime scheduling or set-user-ID files, the
  # system calls of a system service of the native architecture, and programs from the store
  # alone. A unit widens CapabilityBoundingSet and RestrictAddressFamilies to what it needs, and
  # names the paths it writes in ReadWritePaths.
  hardened = noExec // {
    CapabilityBoundingSet = [ "" ];
    RestrictAddressFamilies = [ "AF_UNIX" ];
    NoNewPrivileges = true;
    ProtectSystem = "strict";
    ProtectHome = true;
    PrivateTmp = true;
    PrivateDevices = true;
    ProtectKernelTunables = true;
    ProtectKernelModules = true;
    ProtectKernelLogs = true;
    ProtectControlGroups = true;
    ProtectClock = true;
    ProtectHostname = true;
    RestrictNamespaces = true;
    RestrictRealtime = true;
    RestrictSUIDSGID = true;
    LockPersonality = true;
    MemoryDenyWriteExecute = true;
    SystemCallArchitectures = "native";
    SystemCallFilter = [ "@system-service" ];
  };
  # The type of a command option: a list of the program and its arguments, each a string or a
  # package, whose path it stands for; a string, split at white space; or a package, whose path is
  # the program. argv turns a value of it into the list chalkd runs, so no argument of a list is
  # split.
  commandType =
    lib.types.either (lib.types.coercedTo lib.types.package toString lib.types.nonEmptyStr) (
      lib.types.nonEmptyListOf (lib.types.coercedTo lib.types.package toString lib.types.str)
    )
    // {
      description = "list of a program and its arguments, command or package";
    };
  # The program and arguments of a value of commandType. builtins.split drops the store paths a
  # string refers to, so each word of a string refers to those of the whole string.
  argv =
    command:
    if lib.isList command then
      command
    else
      map (lib.addContextFrom command) (
        lib.filter (w: lib.isString w && w != "") (builtins.split "[[:space:]]+" command)
      );
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

  check = {
    # A command and its arguments, run once, with $OUT in the arguments replaced by the output's
    # directory.
    run = argv: { run = argv; };
    # A command and its arguments, run once for every file of the output that glob matches, with
    # $FILE in the arguments replaced by the file's path and $OUT by the output's directory.
    each = glob: argv: {
      each = glob;
      run = argv;
    };
  };

  validate = {
    # A jq program that reads the input of the documents by kind and makes the report of refusals.
    jq = path: { jq = path; };
  };

  restartOn = effectOn "restart";
  reloadOn = effectOn "reload";

  # The sandboxes of units: noExec lets programs run from the store alone, and hardened adds a
  # service's sandbox without capabilities, which a unit widens as it needs.
  sandbox = {
    inherit noExec hardened;
  };

  # What chalkos's own modules share, which extensions do not call.
  internal =
    refuseLib
    // kindCheck
    // {
      inherit
        api
        kindTypes
        declaredKinds
        ;
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
      # The option <name>.enable of a feature of a layer's images, on unless default is false.
      # Its description says that the layer's images carry `carries` and accept the kinds named,
      # if any; layer is the layer as the description names it, such as router. The option also
      # carries the declaration as chalkosFeature, from which the documentation's features tables
      # are generated.
      feature =
        layer: name:
        {
          carries,
          kinds ? [ ],
          default ? true,
        }:
        let
          names = map (k: "`${k}`") kinds;
          kindsText =
            if lib.length names == 1 then
              "the kind ${lib.head names}"
            else
              "the kinds ${lib.concatStringsSep ", " (lib.init names)} and ${lib.last names}";
          accepts = lib.optionalString (kinds != [ ]) " and accept ${kindsText}";
          how =
            if default then
              "set it false in an image's modules to leave the feature out of it."
            else
              "set it true in an image's modules to add the feature to it.";
        in
        {
          ${name}.enable =
            lib.mkOption {
              type = lib.types.bool;
              inherit default;
              description = "Whether ${layer} images carry ${carries}${accepts}. Only with the ${layer} layer; ${how}";
            }
            // {
              chalkosFeature = {
                inherit carries kinds default;
              };
            };
        };
      # The sandbox of a unit that runs as root but needs no capability and no network: Unix
      # sockets alone, which stay reachable on a read-only system and from the private network
      # namespace, as it hides abstract sockets alone; no home; kernel tunables, modules and
      # cgroups out of reach; and the system calls of a system service of the native
      # architecture. Programs run from the store alone. As root, the unit may still ask systemd
      # over its socket to start any unit.
      rootNoCaps = noExec // {
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
      inherit commandType argv;
      # The names of chalkos's layers, those of the flake's nixosModules.
      layerNames = lib.attrNames (import ./layers.nix);
      # The type of a program option: a value of commandType, or an attribute set of a builder,
      # such as chalkosLib.render's, which the option's module checks.
      programType =
        builders:
        lib.types.either commandType (lib.types.attrsOf lib.types.raw)
        // {
          description = "list of a program and its arguments, command, package or ${builders} value";
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
