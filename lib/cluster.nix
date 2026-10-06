# mkCluster evaluates a cluster definition: chalkos's cluster modules plus the user's modules.
# The result is the evaluated `chalkos` attribute set (cluster, roles, nodes, manifest, ...).
{ nixpkgs }:
{
  modules,
  specialArgs ? { },
}:
(nixpkgs.lib.evalModules {
  modules = [ ../modules/cluster ] ++ modules;
  specialArgs = {
    inherit nixpkgs;
  }
  // specialArgs;
}).config.chalkos
