{
  description = "A chalkos home lab: a router and three Kubernetes nodes on an IPv6 fabric";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { self, chalkos, ... }:
    let
      system = "x86_64-linux";
      pkgs = chalkos.inputs.nixpkgs.legacyPackages.${system};
      # Three images for bare metal: the router's, without Kubernetes; the nodes', whose
      # Kubernetes layer announces their addresses over BGP; and node-bird-metal, the nodes' image
      # with the built-in BGP left out and the bird extension of extensions/ in its place, which
      # docs/guides/bgp-extension.md builds.
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
          {
            name = "node-bird-metal";
            platform = "metal";
            modules = [
              chalkos.nixosModules.kubernetes
              { chalkos.kubernetes.bgp.enable = false; }
              ./extensions
            ];
          }
        ];
      };
    in
    {
      # nix build .#<image> -o images/<image>: the images where the inventory, chalkos.yaml,
      # points; node-bird-metal for the BGP extension's guide. nix build .#installer: the
      # installer's raw image and ISO, which install them.
      packages.${system} = {
        inherit (images)
          router-metal
          node-metal
          node-bird-metal
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
        ];
      };
    };
}
