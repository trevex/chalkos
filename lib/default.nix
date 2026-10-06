{ nixpkgs }:
{
  mkCluster = import ./cluster.nix { inherit nixpkgs; };
}
