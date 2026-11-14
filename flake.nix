{
  description = "chalkos: an image-based NixOS for Kubernetes nodes";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  # The Go toolchain: the Go project's release that go.mod names.
  inputs.go-overlay = {
    url = "github:purpleclay/go-overlay";
    inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      go-overlay,
    }:
    let
      system = "x86_64-linux";
      overlay = import ./nix/overlay.nix { inherit go-overlay; };
      pkgs = nixpkgs.legacyPackages.${system}.extend overlay;
      chalkos = import ./lib { inherit nixpkgs overlay; };
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
      lib = chalkos;
      overlays.default = overlay;
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
