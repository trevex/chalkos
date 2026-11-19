# chalkos's library, the flake's lib output: the helpers the images' modules get as chalkosLib,
# here built from chalkos's nixpkgs for code outside an image, the functions of images.nix, which
# build a cluster's images, and the Kubernetes releases chalkos pins.
{ nixpkgs, overlay }:
import ./chalkos.nix { inherit (nixpkgs) lib; }
// import ./images.nix {
  defaultNixpkgs = nixpkgs;
  inherit overlay;
}
// {
  # Each Kubernetes minor an image can run, as { version; hash; publish; }: its pinned patch
  # release, the hash of its source, and whether a release of chalkos builds images of it.
  kubernetesReleases = builtins.mapAttrs (_: release: { publish = false; } // release) (
    import ../modules/node/kubernetes/releases.nix
  );
}
