{
  description = "A chalkos router and a client on its LAN that chalklab runs on this machine";

  inputs.chalkos.url = "github:trevex/chalkos/v0.3.0"; # x-release-please-version

  outputs =
    { self, chalkos, ... }:
    let
      system = "x86_64-linux";
      pkgs = chalkos.inputs.nixpkgs.legacyPackages.${system};
      # Images of your own, in place of the published router image chalkos.yaml names: the
      # router's, with the router layer, and the client's, a node without a layer that stands for a
      # machine on the LAN. Both carry the OS CA of secrets.pub.json, the public part of the
      # secrets chalkctl gen secrets writes, so in maintenance mode they accept only the cluster's
      # clients.
      images = chalkos.lib.mkImages {
        osCA = ./secrets.pub.json;
        # The commit the images are built from, which chalkctl shows beside an image's root hash.
        revision = self.rev or self.dirtyRev or null;
        images = [
          {
            name = "router-kvm";
            platform = "kvm";
            modules = [ chalkos.nixosModules.router ];
          }
          {
            name = "client-kvm";
            platform = "kvm";
            modules = [ ];
          }
        ];
      };
    in
    {
      # nix build .#<image> -o images/<image>: the images where the inventory, chalkos.yaml,
      # points once its sources name the directories.
      packages.${system} = {
        inherit (images) router-kvm client-kvm;
      };

      # The images' evaluated NixOS systems, whose build outputs a guide builds on their own, such
      # as an image's schemas:
      # nix build .#nixosConfigurations.router-kvm.config.system.build.chalkosSchemas
      nixosConfigurations = chalkos.lib.nixosOf images;

      # nix flake check: the images' tests, chalkos.tests.
      checks.${system} = chalkos.lib.testsOf images;

      # nix develop: chalklab and chalkctl of the chalkos this flake pins.
      devShells.${system}.default = pkgs.mkShellNoCC {
        packages = [
          chalkos.packages.${system}.chalklab
          chalkos.packages.${system}.chalkctl
        ];
      };
    };
}
