{
  description = "chalkos extension kinds: a node's rack position and hardware";

  inputs.chalkos.url = "github:trevex/chalkos/v0.2.1"; # x-release-please-version

  outputs =
    { self, chalkos, ... }:
    let
      # One image for bare metal without a layer; default.nix imports the Rack and Hardware
      # extensions.
      images = chalkos.lib.mkImages {
        # The commit the images are built from, which chalkctl shows beside an image's root hash.
        revision = self.rev or self.dirtyRev or null;
        images = [
          {
            name = "node-metal";
            platform = "metal";
            modules = [ ./. ];
          }
        ];
      };
    in
    {
      # nix build .#node-metal -o images/node-metal: the image where the inventory, chalkos.yaml,
      # points.
      packages.x86_64-linux.node-metal = images.node-metal;

      # The image's evaluated NixOS system, whose build outputs a guide builds on its own, such as
      # the image's schemas:
      # nix build .#nixosConfigurations.node-metal.config.system.build.chalkosSchemas
      nixosConfigurations = chalkos.lib.nixosOf images;

      # nix flake check: the image's tests, chalkos.tests.
      checks.x86_64-linux = chalkos.lib.testsOf images;
    };
}
