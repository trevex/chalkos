# Diagnostics: read-only commands of the image's store that chalkd runs for a client of the node
# API, each as a transient unit with a sandbox of its own, which chalkctl node diag lists and runs.
# Every image has the base's (base/diagnostics.nix); layers and extensions declare more. The image
# writes them to /etc/chalkos/diagnostics.json, which changes only with the image. The build
# refuses a declaration chalkd could not run: here what the module system knows, in chalkos-rules
# what chalkd checks as it reads the file.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (lib) mkOption types;
  # The form of a diagnostic's name and a parameter's, and a parameter's place in an argument.
  nameForm = "[a-z][a-z0-9-]*";
  placeholder = "[{](${nameForm})[}]";
  # The longest name of a diagnostic, which the unit names of its runs hold.
  maxName = 64;
  # The form of a group a diagnostic's process belongs to.
  groupForm = "[a-z_][a-z0-9_-]*";
  # The form of a diagnostic's network namespace: a name below /run/netns of letters, digits, _, .
  # and -, and placeholders, that starts with neither . nor -, so it never leaves the directory.
  namespaceForm = "([A-Za-z0-9_]|${placeholder})([A-Za-z0-9_.-]|${placeholder})*";
  # The longest name of a file below /run/netns, which a filled network namespace takes.
  maxNamespace = 255;
  # The network namespace ns with each parameter filled by one character, as chalkd checks its
  # length.
  filled =
    ns: lib.concatMapStrings (m: if lib.isList m then "x" else m) (builtins.split placeholder ns);
  # A timeout in seconds, of the form the option takes.
  seconds =
    t:
    let
      n = lib.toInt (lib.substring 0 (lib.stringLength t - 1) t);
    in
    if lib.hasSuffix "m" t then n * 60 else n;

  parameter = types.submodule {
    options = {
      type = mkOption {
        type = types.enum [
          "address"
          "prefix"
          "hostname"
          "interface"
          "int"
          "enum"
          "pattern"
        ];
        example = "address";
        description = ''
          What the value is. chalkd refuses a value of another type, and passes the command its
          canonical form: `address` is an IPv4 or IPv6 address, with a zone for a link-local one;
          `prefix` an address with a prefix length; `hostname` a DNS name, which the command gets
          fully qualified, with a trailing dot; `interface` a network interface's name; `int` a
          decimal integer from `min` to `max`; `enum` one of `values`; `pattern` a string that
          `regex` matches whole. chalkd refuses any value that starts with `-` or holds a control
          character.
        '';
      };
      description = mkOption {
        type = types.nonEmptyStr;
        example = "IPv4 or IPv6 address.";
        description = "One sentence on the value, which `chalkctl node diag` lists.";
      };
      optional = mkOption {
        type = types.bool;
        default = false;
        description = "Whether a call may leave the parameter out, which drops every argument of `command` that names it.";
      };
      default = mkOption {
        type = types.nullOr types.nonEmptyStr;
        default = null;
        example = "main";
        description = "Value the parameter takes when a call leaves it out, of the parameter's type; null for none. A parameter with a default is not `optional` as well.";
      };
      min = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Least value of an `int` parameter; null for none.";
      };
      max = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Greatest value of an `int` parameter; null for none.";
      };
      values = mkOption {
        type = types.listOf types.nonEmptyStr;
        default = [ ];
        example = [
          "inet"
          "inet6"
        ];
        description = "Values of an `enum` parameter.";
      };
      regex = mkOption {
        type = types.nullOr types.nonEmptyStr;
        default = null;
        example = "^[a-z0-9]+$";
        description = "Regular expression in Go's RE2 syntax, from `^` to `$`, that the value of a `pattern` parameter matches whole.";
      };
    };
  };

  diagnostic = types.submodule {
    options = {
      description = mkOption {
        type = types.nonEmptyStr;
        example = "The route the kernel takes to an address.";
        description = "One sentence on what the diagnostic shows, which `chalkctl node diag` lists.";
      };
      command = mkOption {
        type = types.nonEmptyListOf types.str;
        example = lib.literalExpression ''[ "''${pkgs.iproute2}/bin/ip" "-json" "route" "get" "{address}" ]'';
        description = ''
          The command chalkd runs, as a list of arguments that no shell reads. The first is a
          program of the image's store. `{<parameter>}` in an argument becomes the parameter's
          value within that argument alone, and an argument that names an optional parameter a
          call leaves out is dropped. Every placeholder names a parameter, and every parameter
          has a placeholder.
        '';
      };
      parameters = mkOption {
        type = types.attrsOf parameter;
        default = { };
        description = "The values a call passes, by names of the form `[a-z][a-z0-9-]*`.";
      };
      role = mkOption {
        type = types.enum [
          "reader"
          "operator"
          "admin"
        ];
        default = "operator";
        description = "Least client role that runs the diagnostic, which chalkd checks on top of the reader role that RunDiagnostic needs.";
      };
      timeout = mkOption {
        type = types.strMatching "[1-9][0-9]*[sm]";
        default = "30s";
        example = "10s";
        description = "How long a run may take, in seconds or minutes, from `1s` to `10m`; systemd stops the run then.";
      };
      capabilities = mkOption {
        type = types.listOf (types.strMatching "CAP_[A-Z_]+");
        default = [ ];
        example = [ "CAP_NET_RAW" ];
        description = "Capabilities of the run's process, as its bounding and ambient sets; it has no other.";
      };
      groups = mkOption {
        type = types.listOf types.nonEmptyStr;
        default = [ ];
        example = [ "bird" ];
        description = "Groups of the image the run's process belongs to, as for a daemon's control socket.";
      };
      networkNamespace = mkOption {
        type = types.nullOr (types.strMatching namespaceForm);
        default = null;
        example = "blue-{name}";
        description = "Network namespace the run joins, by its name below `/run/netns`, which may name parameters as `command` does; null runs it in the host's network. The name holds letters, digits, `_`, `.` and `-`, starts with neither `.` nor `-`, and fills to 255 characters at most; the build counts each parameter as one character, and chalkd refuses a call whose values fill it to anything else, and fails one whose namespace does not exist before the command runs. A parameter it names is not `optional`.";
      };
    };
  };

  # The parameters an argument names.
  placeholders =
    arg: lib.concatMap (m: if lib.isList m then m else [ ]) (builtins.split placeholder arg);

  # The build's refusals of a diagnostic, as messages that name its options.
  problems =
    name: d:
    let
      where = "chalkos.diagnostics.${name}";
      program = lib.head d.command;
      named = lib.unique (
        lib.concatMap placeholders (
          lib.tail d.command ++ lib.optional (d.networkNamespace != null) d.networkNamespace
        )
      );
      inNamespace = lib.optionals (d.networkNamespace != null) (placeholders d.networkNamespace);
      parameters = lib.attrNames d.parameters;
    in
    lib.optional (
      builtins.match nameForm name == null
    ) "${where}: the name is not of the form ${nameForm}"
    ++ lib.optional (
      lib.stringLength name > maxName
    ) "${where}: the name is longer than ${toString maxName} characters"
    ++ lib.optional (seconds d.timeout > 600) "${where}.timeout: ${d.timeout} is longer than 10m"
    ++
      lib.optional
        (d.networkNamespace != null && lib.stringLength (filled d.networkNamespace) > maxNamespace)
        "${where}.networkNamespace: it is longer than ${toString maxNamespace} characters, each parameter counted as one"
    ++ lib.optional (
      !lib.hasPrefix "${builtins.storeDir}/" program
    ) "${where}.command: its first argument, ${program}, is no program of the image's store"
    ++ map (p: "${where}: the command or networkNamespace names {${p}}, which is no parameter") (
      lib.subtractLists parameters named
    )
    ++ map (p: "${where}.parameters.${p}: the command names no {${p}}") (
      lib.subtractLists named parameters
    )
    ++ map (p: "${where}.parameters.${p}: the name is not of the form ${nameForm}") (
      lib.filter (p: builtins.match nameForm p == null) parameters
    )
    ++ map (
      p:
      "${where}.parameters.${p}: it is optional and has a default; a default fills the arguments that name it, optional drops them"
    ) (lib.filter (p: d.parameters.${p}.optional && d.parameters.${p}.default != null) parameters)
    ++ map (p: "${where}.parameters.${p}: networkNamespace names it, so it is not optional") (
      lib.filter (p: lib.elem p inNamespace && d.parameters.${p}.optional) parameters
    )
    ++ map (g: "${where}.groups: ${g} is not of the form ${groupForm}") (
      lib.filter (g: builtins.match groupForm g == null) d.groups
    )
    ++ map (g: "${where}.groups: the image has no group ${g}") (
      lib.filter (g: !(config.users.groups ? ${g})) d.groups
    );

  # A diagnostic as chalkd reads it.
  diagnosticJSON = _: d: {
    inherit (d)
      description
      command
      role
      timeout
      capabilities
      groups
      ;
    networkNamespace = if d.networkNamespace == null then "" else d.networkNamespace;
    parameters = lib.mapAttrs (_: p: {
      inherit (p)
        type
        description
        optional
        min
        max
        values
        ;
      default = if p.default == null then "" else p.default;
      regex = if p.regex == null then "" else p.regex;
    }) d.parameters;
  };
  diagnosticsJSON = builtins.toJSON (lib.mapAttrs diagnosticJSON config.chalkos.diagnostics);
in
{
  options.chalkos.diagnostics = mkOption {
    type = types.attrsOf diagnostic;
    default = { };
    description = ''
      Read-only commands of the image that a client of the node API runs with
      `chalkctl node diag`, by names of the form `[a-z][a-z0-9-]*`. chalkd checks the caller's
      role and each argument against its parameter's type, then runs the command as a transient
      systemd unit, in the host's network or in the network namespace that `networkNamespace`
      names. The unit runs under a dynamic user, without new privileges, with only the
      `capabilities` and `groups` given. It sees a read-only file system with a private `/tmp`,
      without STATE, `/var/lib`, or the config and credentials chalkd writes, and runs for at
      most `timeout`. chalkd sends at most 1 MiB of what the command prints. Every image declares
      the base diagnostics, whose names are taken.
    '';
  };

  config = {
    system.build.chalkosDiagnostics = pkgs.writeText "chalkos-diagnostics.json" diagnosticsJSON;
    environment.etc."chalkos/diagnostics.json".text = diagnosticsJSON;
    assertions = map (message: {
      assertion = false;
      inherit message;
    }) (lib.concatLists (lib.mapAttrsToList problems config.chalkos.diagnostics));
  };
}
