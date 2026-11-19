# image-layout <name>@<digest> <dir>: copies the amd64 manifest of an image into dir as an OCI
# layout, with the manifest's bytes kept. skopeo's index.json differs between its releases, so the
# layout's index is written again with the manifest's descriptor alone, and the layout's hash
# covers the image's own bytes. The test images' fixed-output derivations and update-pins, which
# computes their hashes, both run it.
{ pkgs }:
pkgs.writeShellApplication {
  name = "image-layout";
  runtimeInputs = [
    pkgs.skopeo
    pkgs.jq
  ];
  text = ''
    skopeo --insecure-policy --override-os linux --override-arch amd64 copy --quiet --preserve-digests \
      "docker://$1" "oci:$2"
    jq -c '{schemaVersion: 2, manifests: [.manifests[] | {mediaType, digest, size}]}' \
      "$2/index.json" >"$2/index.json.new"
    mv "$2/index.json.new" "$2/index.json"
  '';
}
