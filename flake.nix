{
  description = "chalkos: an image-based NixOS for Kubernetes nodes";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      chalkos = import ./lib { inherit nixpkgs; };
      # The images the e2e tests boot, those the checks and the documentation evaluate, and the
      # documentation, which the packages, checks and apps share.
      testing = import ./nix/testing/images.nix { inherit self pkgs; };
      fixtures = import ./nix/testing/fixtures.nix { inherit self; };
      docs = import ./nix/docs.nix { inherit pkgs self fixtures; };
      arguments = {
        inherit
          pkgs
          self
          testing
          fixtures
          docs
          ;
      };
    in
    {
      lib = { inherit (chalkos) mkImages; };
      templates.lab = {
        path = ./templates/lab;
        description = "A cluster of a control plane and a worker that chalklab runs as QEMU virtual machines";
      };
      packages.${system} = import ./nix/packages.nix arguments;
      checks.${system} = import ./nix/checks arguments;
      apps.${system} = import ./nix/apps.nix arguments;
      devShells.${system}.default = import ./nix/shell.nix { inherit pkgs self; };
      formatter.${system} = pkgs.nixfmt-tree;
    };
}
