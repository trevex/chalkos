# The cluster settings every image of the cluster reads: the cluster definition minus per-node
# data and outputs. Declaring each setting as a read-only option keeps image modules from
# overriding cluster settings and lets the module system reject unknown names under `chalkos`.
{ lib, chalkos }:
rec {
  settings = removeAttrs chalkos [
    "nodes"
    "roles"
    "installer"
    "manifest"
    "warnings"
  ];
  module = {
    options.chalkos = lib.mkOption {
      type = lib.types.submodule {
        options = lib.mapAttrs (
          _: _:
          lib.mkOption {
            type = lib.types.anything;
            readOnly = true;
          }
        ) settings;
      };
    };
    config.chalkos = settings;
  };
}
