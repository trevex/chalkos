# The images a release publishes. release-eval evaluates each of them and holds them to the
# repositories, tags and annotations of a release, without building any. Each image's
# release-image-<name> check, with its name's dots as dashes, builds the image and its OCI image
# layout, whose manifest carries the annotations its metadata.json names, and its SBOM, which it
# validates. A KVM image's check also builds its containerDisk and checks its layout, its layer and
# that its annotations name the image's root hash. sbom builds and validates the SBOMs of the router
# test image, which the image checks build too, and of chalkctl and chalkbuild, so a broken SBOM
# fails before a release.
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
        disk = image.containerDisk or "";
        systemRegion = toString image.nixos.config.chalkos.disk.systemRegionSize;
      }
      ''
        l=${image.oci}
        blob() { echo "$l/blobs/sha256/''${1#sha256:}"; }
        m=$(blob "$(jq -r '.manifests[0].digest' $l/index.json)")
        jq -e --argjson want "$expected" '.annotations == $want' $m
        jq -e --slurpfile md ${image}/metadata.json '.annotations == $md[0].annotations' $m
        ${sbom.validate}
        validate-spdx ${image.sbom}

        # A KVM image's containerDisk: a valid layout of one manifest whose blobs match their
        # digests, with one layer holding disk/chalkos.qcow2 alone, owned by 107:107 and as large as
        # the system region, and the image's annotations, its name and the root hash of its header.
        if [ -n "$disk" ]; then
          rootHash=$(jq -r .rootHash "$(blob "$(jq -r .config.digest $m)")")
          l=$disk
          jq -e '.imageLayoutVersion == "1.0.0"' $l/oci-layout
          jq -e '.manifests | length == 1' $l/index.json
          (cd $l/blobs/sha256 && for b in *; do echo "$b  $b"; done | sha256sum --check --quiet)
          dm=$(blob "$(jq -r '.manifests[0].digest' $l/index.json)")
          jq -e '.mediaType == "application/vnd.oci.image.manifest.v1+json"
            and .config.mediaType == "application/vnd.oci.image.config.v1+json"
            and (.layers | length == 1)
            and .layers[0].mediaType == "application/vnd.oci.image.layer.v1.tar"' $dm
          layer=$(jq -r '.layers[0].digest' $dm)
          jq -e --arg layer "$layer" '.os == "linux" and .rootfs.diff_ids == [$layer]' \
            "$(blob "$(jq -r .config.digest $dm)")"
          jq -e --argjson want "$expected" --arg name ${image.name} --arg rootHash "$rootHash" \
            '.annotations == $want + {"dev.chalkos.image.name": $name, "dev.chalkos.image.root-hash": $rootHash}' $dm
          entries=$(tar --numeric-owner -tvf "$(blob "$layer")" | awk '{print $2, $6}')
          [ "$entries" = "$(printf '107/107 disk/\n107/107 disk/chalkos.qcow2')" ] || {
            echo "the containerDisk's layer holds, by owner and name: $entries"
            exit 1
          }
          # The qcow2's header: its magic, and its virtual size at offset 24, which is the system
          # region's size, as chalkos.disk sizes it and as the built image lays out ESP and slot A,
          # with room for slot B and STATE behind them.
          # tar stops on the broken pipe once head has the header.
          (tar -xOf "$(blob "$layer")" disk/chalkos.qcow2 || true) | head -c 32 >header
          [ "$(od -An -N4 -tx1 header | tr -d ' \n')" = 514649fb ] || {
            echo "the containerDisk's disk/chalkos.qcow2 is no qcow2 image"
            exit 1
          }
          virtual=$((16#$(od -An -j24 -N8 -tx1 header | tr -d ' \n')))
          state=$(numfmt --from=iec "$(sed -n 's/^SizeMinBytes=//p' ${image}/repart.d/50-state.conf)")
          layout=$(jq --argjson state "$state" \
            '([.[] | .offset + .raw_size] | max) + ([.[] | select(.roothash != null) | .raw_size] | add) + $state + 1048576' \
            ${image}/repart-output.json)
          for want in "$layout" ''${systemRegion:-$layout}; do
            [ "$virtual" = "$want" ] || {
              echo "the containerDisk's disk is $virtual bytes, not the system region's $want (laid out: $layout, chalkos.disk: ''${systemRegion:-null})"
              exit 1
            }
          done
        fi
        touch $out
      ''
  )
) images
