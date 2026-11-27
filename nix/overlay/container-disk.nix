# pkgs.chalkos.containerDisk: a KubeVirt containerDisk of an image directory, as an OCI image
# layout. Its one layer is an uncompressed tar of disk/chalkos.qcow2, the image's raw disk raw
# converted to qcow2, owned by 107:107, the qemu user that KubeVirt runs a containerDisk's VM as.
# The entries are in name order with the modification time 0, so the same image always makes the
# same layout. The manifest carries the image's annotations from its metadata.json, with
# dev.chalkos.image.name and dev.chalkos.image.root-hash naming the image the disk holds. The
# layout is written straight into $out, so the multi-gigabyte disk never passes through the build
# directory.
{
  gnutar,
  jq,
  qemu-utils,
  runCommand,
  stdenv,
}:
{
  image,
  name,
  raw,
}:
runCommand "chalkos-${name}-container-disk"
  {
    nativeBuildInputs = [
      gnutar
      jq
      qemu-utils
    ];
    architecture = stdenv.hostPlatform.go.GOARCH;
  }
  ''
    mkdir -p $out/blobs/sha256 $out/stage/disk
    qemu-img convert -f raw -O qcow2 ${image}/${raw} $out/stage/disk/chalkos.qcow2
    tar --sort=name --mtime=@0 --owner=107 --group=107 --numeric-owner --mode=u=rwX,go=rX \
      -cf $out/layer.tar -C $out/stage disk
    rm -rf $out/stage

    # blob <file> moves the file to the blobs by its digest and prints its descriptor's digest and
    # size.
    blob() {
      digest=$(sha256sum "$1" | cut -d' ' -f1)
      size=$(stat -c %s "$1")
      mv "$1" $out/blobs/sha256/$digest
      echo "sha256:$digest $size"
    }
    read -r layer layerSize < <(blob $out/layer.tar)

    jq -cn --arg diff "$layer" --arg architecture "$architecture" '{
      $architecture,
      os: "linux",
      config: {},
      rootfs: {type: "layers", diff_ids: [$diff]}
    }' >config.json
    read -r config configSize < <(blob config.json)

    jq -c --arg config "$config" --argjson configSize $configSize \
      --arg layer "$layer" --argjson layerSize $layerSize '{
      schemaVersion: 2,
      mediaType: "application/vnd.oci.image.manifest.v1+json",
      config: {mediaType: "application/vnd.oci.image.config.v1+json", digest: $config, size: $configSize},
      layers: [{mediaType: "application/vnd.oci.image.layer.v1.tar", digest: $layer, size: $layerSize}],
      annotations: (.annotations + {
        "dev.chalkos.image.name": .name,
        "dev.chalkos.image.root-hash": .rootHash
      })
    }' ${image}/metadata.json >manifest.json
    read -r manifest manifestSize < <(blob manifest.json)

    jq -cn --arg manifest "$manifest" --argjson manifestSize $manifestSize '{
      schemaVersion: 2,
      manifests: [{mediaType: "application/vnd.oci.image.manifest.v1+json", digest: $manifest, size: $manifestSize}]
    }' >$out/index.json
    echo '{"imageLayoutVersion":"1.0.0"}' >$out/oci-layout
  ''
