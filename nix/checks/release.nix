# The images a release publishes. release-eval evaluates each of them and holds them to the
# repositories, tags and annotations of a release, without building any. Each image's
# release-image-<name> check, with its name's dots as dashes, builds the image and its OCI image
# layout, whose manifest carries the annotations its metadata.json names, and its SBOM, which it
# validates. sbom builds and validates the SBOMs of the router test image, which the image checks
# build too, and of chalkctl and chalkbuild, so a broken SBOM fails before a release.
{
  pkgs,
  self,
  checkLib,
  testing,
  sbom,
  ...
}:
let
  inherit (pkgs) lib;
  inherit (pkgs.stdenv.hostPlatform) system;
  images = self.releaseImages.${system};
in
{
  release-eval = checkLib.evalTests "release-eval" "release evaluation" (
    import ../../test/nix/release.nix { inherit lib self images; }
  );

  sbom =
    pkgs.runCommand "chalkos-sbom"
      {
        nativeBuildInputs = sbom.validateInputs;
        image = testing.router-kvm.sbom;
        chalkctl = self.packages.${system}.chalkctl.sbom;
        chalkbuild = self.packages.${system}.chalkbuild.sbom;
      }
      ''
        ${sbom.validate}
        validate-spdx "$image"
        validate-cyclonedx "$chalkctl"
        validate-cyclonedx "$chalkbuild"
        touch $out
      '';
}
// lib.mapAttrs' (
  name: image:
  lib.nameValuePair "release-image-${lib.replaceStrings [ "." ] [ "-" ] name}" (
    pkgs.runCommand "chalkos-release-image-${name}"
      {
        nativeBuildInputs = sbom.validateInputs;
        expected = builtins.toJSON image.nixos.config.chalkos.imageDirectory.annotations;
      }
      ''
        l=${image.oci}
        blob() { echo "$l/blobs/sha256/''${1#sha256:}"; }
        m=$(blob "$(jq -r '.manifests[0].digest' $l/index.json)")
        jq -e --argjson want "$expected" '.annotations == $want' $m
        jq -e --slurpfile md ${image}/metadata.json '.annotations == $md[0].annotations' $m
        ${sbom.validate}
        validate-spdx ${image.sbom}
        touch $out
      ''
  )
) images
