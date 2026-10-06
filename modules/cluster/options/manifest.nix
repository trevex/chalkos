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

  node = name: n: {
    inherit (n) role;
    identity = {
      inherit (n) hostname network labels;
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
