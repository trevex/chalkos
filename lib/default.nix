# chalkos's library, the flake's lib output: the helpers the images' modules get as chalkosLib,
# here built from chalkos's nixpkgs for code outside an image, the functions of images.nix, which
# build a cluster's images, the Kubernetes releases chalkos pins and chalkos's version. bombon
# builds each image's SBOM.
{
  nixpkgs,
  overlay,
  bombon,
}:
import ./chalkos.nix { inherit (nixpkgs) lib; }
// import ./images.nix {
  defaultNixpkgs = nixpkgs;
  inherit overlay bombon;
}
// {
  # Each Kubernetes minor an image can run, as { version; hash; publish; }: its pinned patch
  # release, the hash of its source, and whether a release of chalkos builds images of it.
  kubernetesReleases = builtins.mapAttrs (_: release: { publish = false; } // release) (
    import ../modules/node/kubernetes/releases.nix
  );
  # chalkos's version, from version.nix, such as "0.1.0".
  version = import ../version.nix;
}
