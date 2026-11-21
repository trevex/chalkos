{
  description = "A chalkos home lab: a router and three Kubernetes nodes on an IPv6 fabric";

  inputs.chalkos.url = "github:trevex/chalkos/v0.3.2"; # x-release-please-version

  outputs =
    { self, chalkos, ... }:
    let
      system = "x86_64-linux";
      pkgs = chalkos.inputs.nixpkgs.legacyPackages.${system};
      # The router and Kubernetes node images use their layers' built-in BGP.
      images = chalkos.lib.mkImages {
        # The commit the images are built from, which chalkctl shows beside an image's root hash.
        revision = self.rev or self.dirtyRev or null;
        images = [
          {
            name = "router-metal";
            platform = "metal";
            modules = [ chalkos.nixosModules.router ];
          }
          {
            name = "node-metal";
            platform = "metal";
            modules = [ chalkos.nixosModules.kubernetes ];
          }
        ];
      };
    in
    {
      # nix build .#<image> -o images/<image>: the images where the inventory, chalkos.yaml,
      # points. nix build .#installer builds the raw image and ISO that install them.
      packages.${system} = {
        inherit (images)
          router-metal
          node-metal
          installer
          ;
      };

      # The images' evaluated NixOS systems, whose build outputs a guide builds on their own, such
      # as an image's schemas:
      # nix build .#nixosConfigurations.node-metal.config.system.build.chalkosSchemas
      nixosConfigurations = chalkos.lib.nixosOf images;

      # nix flake check: the images' tests, chalkos.tests.
      checks.${system} = chalkos.lib.testsOf images;

      # nix develop: chalkctl and chalkbuild of the chalkos this flake pins, and the other tools
      # the guide runs.
      devShells.${system}.default = pkgs.mkShellNoCC {
        packages = [
          chalkos.packages.${system}.chalkctl
          chalkos.packages.${system}.chalkbuild
          pkgs.kubectl
          pkgs.jq
          pkgs.openssl
          pkgs.wireguard-tools
          pkgs.arp-scan
          pkgs.netcat
          # The air-gapped lab copies the cluster's container images with these.
          pkgs.crane
          pkgs.yq-go
        ];
      };
    };
}
