{
  description = "chalkos: an image-based NixOS for Kubernetes nodes";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      chalkosLib = import ./lib { inherit nixpkgs; };
    in
    {
      lib = { inherit (chalkosLib) mkCluster; };
      flakeModules.default = chalkosLib.flakeModule;
      packages.${system} = import ./nix/packages.nix { inherit pkgs self; };
      checks.${system} = import ./nix/checks.nix { inherit pkgs self; };
      devShells.${system}.default = import ./nix/shell.nix { inherit pkgs self; };
      formatter.${system} = pkgs.nixfmt-tree;
    };
}
