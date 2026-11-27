# pkgs.chalkos.containerDisk: a KubeVirt containerDisk of an image directory, as an OCI image
# layout. Its one layer is an uncompressed tar of disk/chalkos.qcow2, the image's raw disk raw
# converted to qcow2 and grown to size bytes, owned by 107:107, the qemu user that KubeVirt runs a
# containerDisk's VM as. The raw disk holds the ESP and slot A alone, and a first boot adds slot B
# and STATE behind them, so size is the whole system region, chalkos.disk.systemRegionSize. When
# that is null, as a store size is, the disk makes room for slot B as large as slot A and for
# STATE as its repart.d definition sizes it. The qcow2 stays sparse, so the room costs the layer
# nothing. The entries are in name order with the modification time 0, so the same image always
# makes the same layout. The manifest carries the image's annotations from its metadata.json, with
# dev.chalkos.image.name and dev.chalkos.image.root-hash naming the image the disk holds. The
# layout is written straight into $out, so the multi-gigabyte disk never passes through the build
# directory.
{
  gnutar,
  jq,
  lib,
  qemu-utils,
  runCommand,
  stdenv,
}:
{
  image,
  name,
  raw,
  size,
}:
runCommand "chalkos-${name}-container-disk"
  {
    nativeBuildInputs = [
      gnutar
      jq
      qemu-utils
    ];
    architecture = stdenv.hostPlatform.go.GOARCH;
    size = lib.optionalString (size != null) (toString size);
  }
  ''
    if [ -z "$size" ]; then
      state=$(numfmt --from=iec "$(sed -n 's/^SizeMinBytes=//p' ${image}/repart.d/50-state.conf)")
      size=$(jq --argjson state "$state" \
        '([.[] | .offset + .raw_size] | max) + ([.[] | select(.roothash != null) | .raw_size] | add) + $state + 1048576' \
        ${image}/repart-output.json)
    fi
    mkdir -p $out/blobs/sha256 $out/stage/disk
    qemu-img convert -f raw -O qcow2 ${image}/${raw} $out/stage/disk/chalkos.qcow2
    qemu-img resize -f qcow2 $out/stage/disk/chalkos.qcow2 "$size"
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
