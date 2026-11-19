# The images a release publishes. release-eval evaluates each of them and holds them to the
# repositories, tags and annotations of a release, without building any. Each image's
# release-image-<name> check, with its name's dots as dashes, builds the image and its OCI image
# layout, whose manifest carries the annotations its metadata.json names.
{
  pkgs,
  self,
  checkLib,
  ...
}:
let
  inherit (pkgs) lib;
  images = self.releaseImages.${pkgs.stdenv.hostPlatform.system};
in
{
  release-eval = checkLib.evalTests "release-eval" "release evaluation" (
    import ../../test/nix/release.nix { inherit lib self images; }
  );
}
// lib.mapAttrs' (
  name: image:
  lib.nameValuePair "release-image-${lib.replaceStrings [ "." ] [ "-" ] name}" (
    pkgs.runCommand "chalkos-release-image-${name}"
      {
        nativeBuildInputs = [ pkgs.jq ];
        expected = builtins.toJSON image.nixos.config.chalkos.imageDirectory.annotations;
      }
      ''
        l=${image.oci}
        blob() { echo "$l/blobs/sha256/''${1#sha256:}"; }
        m=$(blob "$(jq -r '.manifests[0].digest' $l/index.json)")
        jq -e --argjson want "$expected" '.annotations == $want' $m
        jq -e --slurpfile md ${image}/metadata.json '.annotations == $md[0].annotations' $m
        touch $out
      ''
  )
) images
