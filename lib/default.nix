{ nixpkgs }:
rec {
  mkCluster = import ./cluster.nix { defaultNixpkgs = nixpkgs; };
  mkImages = import ./images.nix { defaultNixpkgs = nixpkgs; };
  flakeModule = import ./flake-module.nix { inherit mkCluster; };
}
