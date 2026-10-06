# flake-parts module: clusters declared under `chalkos.clusters.<name>` become `flake.chalkos.<name>`.
{ mkCluster }:
{ config, lib, ... }:
{
  options.chalkos.clusters = lib.mkOption {
    type = lib.types.attrsOf lib.types.deferredModule;
    default = { };
    description = "Cluster definitions, each evaluated with mkCluster; the attribute name is the cluster name.";
  };

  config.flake.chalkos = lib.mapAttrs (
    name: module:
    mkCluster {
      modules = [
        module
        { chalkos.cluster.name = lib.mkDefault name; }
      ];
    }
  ) config.chalkos.clusters;
}
