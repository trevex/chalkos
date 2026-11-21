{
  description = "chalkos: a toolkit that builds API-driven, immutable Linux distributions from NixOS modules";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  # The Go toolchain: the Go project's release that go.mod names.
  inputs.go-overlay = {
    url = "github:purpleclay/go-overlay";
    inputs.nixpkgs.follows = "nixpkgs";
  };
  # The software bills of materials of the images: bombon lists the components of a closure with
  # their nixpkgs metadata.
  inputs.bombon = {
    url = "github:nikstur/bombon";
    inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      go-overlay,
      bombon,
    }:
    let
      system = "x86_64-linux";
      overlay = import ./nix/overlay { inherit go-overlay; };
      pkgs = nixpkgs.legacyPackages.${system}.extend overlay;
      chalkos = import ./lib { inherit nixpkgs overlay bombon; };
      # The images the e2e tests boot, those the checks and the documentation evaluate, and the
      # documentation, which the packages, checks and apps share.
      testing = import ./nix/testing/images.nix { inherit self pkgs; };
      fixtures = import ./nix/testing/fixtures.nix { inherit self; };
      docs = import ./nix/docs { inherit pkgs self fixtures; };
      # The SBOMs of chalkos's programs and the validation of SBOMs; an image's is its sbom.
      sbom = import ./nix/sbom.nix {
        inherit pkgs;
        bombon = import bombon { inherit pkgs; };
      };
      arguments = {
        inherit
          pkgs
          self
          testing
          fixtures
          docs
          sbom
          ;
      };
    in
    {
      lib = chalkos;
      # The layers, which an image lists in its modules: kubernetes and router.
      nixosModules = import ./lib/layers.nix;
      overlays.default = overlay;
      templates.lab = {
        path = ./templates/lab;
        description = "A cluster of a control plane and a worker that chalklab runs as QEMU virtual machines";
      };
      templates.router-lab = {
        path = ./templates/router-lab;
        description = "A router and a client on its LAN that chalklab runs as QEMU virtual machines";
      };
      templates.cluster-metal = {
        path = ./templates/cluster-metal;
        description = "A Kubernetes cluster of three control planes on real machines, with its installer";
      };
      packages.${system} = import ./nix/outputs/packages.nix arguments;
      # The images a release publishes, by repository name, each with its repository and tag.
      releaseImages.${system} = import ./nix/release.nix {
        inherit self;
        inherit (pkgs) lib;
      };
      checks.${system} = import ./nix/checks arguments;
      # The names of the checks by the tier CI runs them in, which its workflow reads.
      ciTiers.${system} = import ./nix/checks/tiers.nix {
        inherit (pkgs) lib;
        names = builtins.attrNames self.checks.${system};
      };
      apps.${system} = import ./nix/outputs/apps.nix arguments;
      devShells.${system}.default = import ./nix/outputs/shell.nix { inherit pkgs self; };
      formatter.${system} = pkgs.nixfmt-tree;
    };
}
