# flake-parts module: clusters declared under `chalkos.clusters.<name>` become `flake.chalkos.<name>`.
{ mkCluster }:
{ config, lib, ... }:
{
  options.chalkos = {
    clusters = lib.mkOption {
      type = lib.types.attrsOf lib.types.deferredModule;
      default = { };
      description = "Cluster definitions, each evaluated with mkCluster; the attribute name is the cluster name.";
    };
    nixpkgs = lib.mkOption {
      type = lib.types.nullOr lib.types.raw;
      default = null;
      description = "nixpkgs flake used to build role images; null uses the one chalkos was built with.";
    };
  };

  config.flake.chalkos = lib.mapAttrs (
    name: module:
    mkCluster (
      {
        modules = [
          module
          { chalkos.cluster.name = lib.mkDefault name; }
        ];
      }
      // lib.optionalAttrs (config.chalkos.nixpkgs != null) { inherit (config.chalkos) nixpkgs; }
    )
  ) config.chalkos.clusters;
}
