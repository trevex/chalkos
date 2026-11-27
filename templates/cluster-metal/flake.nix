{
  description = "A chalkos Kubernetes cluster of three control planes on real machines";

  inputs.chalkos.url = "github:trevex/chalkos/v0.10.0"; # x-release-please-version

  outputs =
    { self, chalkos, ... }:
    let
      system = "x86_64-linux";
      pkgs = chalkos.inputs.nixpkgs.legacyPackages.${system};
      # The image every node runs, for bare metal, and the installer that installs it. Both carry
      # the OS CA of secrets.pub.json, the public part of the secrets chalkctl gen secrets writes,
      # so a machine that waits in the installer accepts only the cluster's clients.
      images = chalkos.lib.mkImages {
        osCA = ./secrets.pub.json;
        # The commit the images are built from, which chalkctl shows beside an image's root hash.
        revision = self.rev or self.dirtyRev or null;
        images = [
          {
            name = "node-metal";
            platform = "metal";
            modules = [ chalkos.nixosModules.kubernetes ];
          }
        ];
      };
    in
    {
      # nix build .#node-metal -o images/node-metal: the image where the inventory, chalkos.yaml,
      # points. nix build .#installer -o installer: the installer's raw image and ISO.
      packages.${system} = {
        inherit (images) node-metal installer;
      };

      # The image's evaluated NixOS system, whose build outputs a guide builds on its own, such as
      # the image's schemas:
      # nix build .#nixosConfigurations.node-metal.config.system.build.chalkosSchemas
      nixosConfigurations = chalkos.lib.nixosOf images;

      # nix flake check: the image's tests, chalkos.tests.
      checks.${system} = chalkos.lib.testsOf images;

      # nix develop: chalkctl and chalkbuild of the chalkos this flake pins, kubectl, and jq for the
      # JSON of chalkctl's diagnostics.
      devShells.${system}.default = pkgs.mkShellNoCC {
        packages = [
          chalkos.packages.${system}.chalkctl
          chalkos.packages.${system}.chalkbuild
          pkgs.kubectl
          pkgs.jq
        ];
      };
    };
}
