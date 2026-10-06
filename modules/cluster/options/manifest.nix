# The cluster description chalkctl reads. Unstable while schemaVersion is 0.
{ config, lib, ... }:
let
  cfg = config.chalkos;
  storage = import ../storage.nix { inherit lib; };
  coreNodeOptions = [
    "role"
    "hostname"
    "storage"
    "network"
    "labels"
    "taints"
    "_module"
  ];
  strip = attrs: removeAttrs attrs [ "_module" ];

  renderStorage =
    name: n:
    let
      errors = storage.errors n.storage;
    in
    if errors != [ ] then
      throw "chalkos.nodes.${name}.storage is invalid:\n${
        lib.concatMapStringsSep "\n" (e: "- ${e}") errors
      }"
    else
      storage.render {
        cluster = cfg.cluster.name;
        node = name;
        inherit (n) storage;
      };

  # The node's networkd configuration as unit files, rendered with the role image's own networkd
  # options and renderer, so a unit means on the node exactly what it means in NixOS.
  renderNetwork =
    name: n:
    let
      nixos = cfg.roles.${n.role}.nixos;
      inherit (nixos._module.args.utils.systemdUtils.network) units;
      kinds = {
        networks = {
          suffix = "network";
          toUnit = units.networkToUnit;
        };
        netdevs = {
          suffix = "netdev";
          toUnit = units.netdevToUnit;
        };
        links = {
          suffix = "link";
          toUnit = units.linkToUnit;
        };
      };
      unknown = lib.attrNames (removeAttrs n.network (lib.attrNames kinds));
      render =
        kind:
        { suffix, toUnit }:
        let
          defs = n.network.${kind} or { };
          loc = [
            "chalkos"
            "nodes"
            name
            "network"
            kind
          ];
          # Merging through the option's type applies defaults and shorthands such as `name`.
          merged = nixos.options.systemd.network.${kind}.type.merge loc [
            {
              file = "chalkos.nodes.${name}.network";
              value = defs;
            }
          ];
        in
        lib.optionalAttrs (defs != { }) (
          lib.mapAttrs' (unit: def: lib.nameValuePair "${unit}.${suffix}" (toUnit def)) (
            lib.filterAttrs (_: def: def.enable) merged
          )
        );
    in
    if unknown != [ ] then
      throw "chalkos.nodes.${name}.network: ${lib.concatStringsSep ", " unknown} is not one of networks, netdevs and links"
    else
      lib.concatMapAttrs render kinds;

  node = name: n: {
    inherit (n) role;
    identity = {
      inherit (n) hostname network labels;
      networkUnits = renderNetwork name n;
      storage = renderStorage name n;
      taints = map strip n.taints;
      extensions = removeAttrs n coreNodeOptions;
    };
  };
in
{
  options.chalkos.manifest = lib.mkOption {
    type = lib.types.raw;
    readOnly = true;
    description = "Generated cluster description read by chalkctl (JSON-serialisable, schemaVersion 0).";
  };

  config.chalkos.manifest = lib.showWarnings cfg.warnings {
    schemaVersion = 0;
    cluster = { inherit (cfg.cluster) name endpoint; };
    # Relative to the cluster's attribute, which only the evaluating CLI knows; the flake may
    # expose the cluster under any name.
    roles = lib.mapAttrs (role: _: {
      image = "roles.${role}.image";
    }) cfg.roles;
    nodes = lib.mapAttrs node cfg.nodes;
  };
}
