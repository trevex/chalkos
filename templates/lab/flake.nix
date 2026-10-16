{
  description = "A chalkos lab cluster that chalklab runs on this machine";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    let
      system = "x86_64-linux";
      pkgs = chalkos.inputs.nixpkgs.legacyPackages.${system};
    in
    {
      # nix build .#node-kvm -o images/node-kvm: the image both nodes run, where the Image
      # document in cluster/cluster.yaml points.
      packages.${system}.node-kvm =
        (chalkos.lib.mkCluster { modules = [ ./cluster.nix ]; }).roles.node.images.kvm;

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
