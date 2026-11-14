# The image directory mkImages returns: the raw image with slot A labelled by its root hash,
# repart's report, the system region's repart definitions, the schemas, the facts of the image's
# layers and metadata.json. Its build fails when the image leaves no room for the next one, or
# when a derivation of chalkos.imageDirectory.checks fails, such as the image's tests.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.disk;
  # An image leaves room for the next one: the build fails when the store's data takes more than
  # 80% of a slot, its hash tree more than 80% of the slot's verity partition, or the UKIs the ESP
  # holds more than 80% of it. That is three UKIs on an image that is upgraded, both slots' and an
  # upgrade's temporary file, and one otherwise.
  fits =
    let
      name = lib.defaultTo config.image.repart.name config.chalkos.variant.name;
      image = config.system.build.image;
    in
    pkgs.runCommand "${name}-fits"
      {
        nativeBuildInputs = [ pkgs.chalkos.image-size ];
        ukis = if cfg.storeSize == null then 1 else 3;
        storeSize = lib.defaultTo "-" cfg.storeSize;
        storeVeritySize = lib.defaultTo "-" cfg.storeVeritySize;
        inherit (cfg) espSize;
      }
      ''
        chalkos-image-size fits ${name} ${image}/${config.image.fileName} ${image}/repart-output.json \
          ${config.system.build.uki}/${config.system.boot.loader.ukiFile} "$ukis" "$storeSize" "$storeVeritySize" "$espSize"
        touch $out
      '';
  # What chalkctl reads about an image before it validates config or installs it; the root hash
  # is added from systemd-repart's output once the image is built. The revision and tag reach
  # metadata.json alone, never the store, so they change no root hash.
  metadataFields = pkgs.writeText "chalkos-metadata.json" (
    builtins.toJSON {
      schemaVersion = 0;
      inherit (config.system.image) id;
      inherit (config.chalkos.provenance) revision tag;
      variant = config.chalkos.variant.name;
      platform = config.chalkos.platform.name;
      kinds = config.system.build.chalkosKinds;
    }
  );
  metadata =
    pkgs.runCommand "${config.image.repart.name}-metadata.json" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq --slurpfile repart ${config.system.build.image}/repart-output.json \
          '. + { rootHash: ([$repart[0][] | .roothash // empty] | first) }' \
          ${metadataFields} > $out
      '';
  # The layers' facts as facts/<layer>.json.
  facts = pkgs.linkFarm "${config.image.repart.name}-facts" (
    lib.mapAttrsToList (layer: path: {
      name = "${layer}.json";
      inherit path;
    }) config.chalkos.imageDirectory.facts
  );
in
{
  options.chalkos.imageDirectory.facts = lib.mkOption {
    type = lib.types.attrsOf lib.types.path;
    default = { };
    description = ''
      The facts of the image's layers by layer name, each a JSON file the image directory holds as
      `facts/<layer>.json`. chalkctl reads them with the schemas and checks config against both,
      as a node does against those of its image. A layer without facts sets none.
    '';
  };

  options.chalkos.imageDirectory.checks = lib.mkOption {
    type = lib.types.listOf lib.types.package;
    default = [ ];
    internal = true;
    description = ''
      Derivations the image directory's build depends on, which fail it when they fail. Unlike
      `system.checks`, they stay out of the system's derivation, so they change neither the
      image's content nor its root hash.
    '';
  };

  options.chalkos.provenance = {
    revision = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        The git revision the image is built from, as `metadata.json` records it; mkImages sets
        it from its `revision` argument. It never reaches the store, so it changes no root hash,
        and nothing orders or refuses images by it.
      '';
    };
    tag = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        The git tag the image is built from, as `metadata.json` records it; mkImages sets it
        from its `tag` argument. Like the revision, it only says where the image came from.
      '';
    };
  };

  # A layer's name is a file name in facts/ and the name chalkctl and chalkd look its facts up by.
  config.assertions = map (layer: {
    assertion = builtins.match "[a-z0-9-]+" layer != null;
    message = "chalkos.imageDirectory.facts names the layer \"${layer}\"; a layer's name is lower-case letters, digits and dashes";
  }) (lib.attrNames config.chalkos.imageDirectory.facts);

  config.system.build = {
    chalkosImageFits = fits;
    chalkosMetadata = metadata;
    chalkosImage =
      let
        image = config.system.build.image;
        raw = config.image.fileName;
      in
      pkgs.runCommand "${config.image.repart.name}-image"
        {
          inherit fits;
          inherit (config.chalkos.imageDirectory) checks;
          nativeBuildInputs = [
            pkgs.jq
            pkgs.chalkos.relabel-partitions
          ];
        }
        ''
          mkdir $out
          # Slot A's labels name its store by the root hash's first 16 hex digits, which only the
          # built image knows: the raw image is copied, keeping its holes, and its slot relabelled
          # by the partition UUIDs repart reported, which repart-output.json names as well.
          id=$(jq -r '[.[] | .roothash // empty] | first | .[0:16]' ${image}/repart-output.json)
          cp --sparse=always ${image}/${raw} $out/${raw}
          chmod u+w $out/${raw}
          jq -r --arg id "$id" '.[] | select(.roothash != null)
            | [(.uuid | ascii_downcase), (if (.type | endswith("-verity")) then "store-verity_" else "store_" end) + $id]
            | @tsv' ${image}/repart-output.json | chalkos-relabel-partitions $out/${raw} uuid
          jq --arg id "$id" 'map(
              if .roothash == null then .
              elif (.type | endswith("-verity")) then .label = "store-verity_" + $id
              else .label = "store_" + $id end)' \
            ${image}/repart-output.json > $out/repart-output.json
          ln -s ${config.environment.etc."chalkos/repart.d".source} $out/repart.d
          ln -s ${config.system.build.chalkosSchemas} $out/schemas
          ln -s ${facts} $out/facts
          cp ${metadata} $out/metadata.json
        '';
  };
}
