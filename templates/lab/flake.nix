{
  description = "A chalkos lab cluster that chalklab runs on this machine";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { self, chalkos, ... }:
    let
      system = "x86_64-linux";
      pkgs = chalkos.inputs.nixpkgs.legacyPackages.${system};
      # The image both nodes run; cp1's Node document makes it the control plane. It carries the
      # OS CA of secrets.pub.json, the public part of the secrets chalkctl gen secrets writes, so
      # in maintenance mode it accepts only the cluster's clients.
      images = chalkos.lib.mkImages {
        osCA = ./secrets.pub.json;
        # The commit the images are built from, which chalkctl shows beside an image's root hash.
        revision = self.rev or self.dirtyRev or null;
        platforms = [ "kvm" ];
        variants.node = { };
      };
    in
    {
      # nix build .#node-kvm -o images/node-kvm: the image where the inventory, chalkos.yaml,
      # points.
      packages.${system}.node-kvm = images.images.node-kvm;

      # nix develop: chalklab and chalkctl of the chalkos this flake pins, and kubectl.
      devShells.${system}.default = pkgs.mkShellNoCC {
        packages = [
          chalkos.packages.${system}.chalklab
          chalkos.packages.${system}.chalkctl
          pkgs.kubectl
        ];
      };
    };
}
