# The cluster description chalkctl reads. Unstable while schemaVersion is 0.
{ config, lib, ... }:
let
  cfg = config.chalkos;
  coreNodeOptions = [
    "role"
    "hostname"
    "install"
    "network"
    "labels"
    "taints"
    "_module"
  ];
  strip = attrs: removeAttrs attrs [ "_module" ];

  node =
    name: n:
    if !(cfg.roles ? ${n.role}) then
      throw ''chalkos.nodes.${name}.role is "${n.role}", but the defined roles are: ${lib.concatStringsSep ", " (lib.attrNames cfg.roles)}''
    else
      {
        inherit (n) role;
        install.disk = n.install.disk;
        identity = {
          inherit (n) hostname network labels;
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

  config.chalkos.manifest = {
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
