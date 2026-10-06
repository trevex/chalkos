{ nixpkgs }:
rec {
  mkCluster = import ./cluster.nix { inherit nixpkgs; };
  flakeModule = import ./flake-module.nix { inherit mkCluster; };
}
