# Renders a node's storage options into the `storage` section of its identity: repart.d
# definitions per disk and a description of each volume, which chalkos-storage applies on the node.
{ lib }:
let
  # A UUID-shaped hash, so every evaluation renders the same UUID for the same input.
  uuidOf =
    input:
    let
      h = builtins.hashString "sha256" input;
      part = start: len: builtins.substring start len h;
    in
    "${part 0 8}-${part 8 4}-${part 12 4}-${part 16 4}-${part 20 12}";

  # repart assigns existing partitions to definitions by type, then in file name order. A type
  # per label keeps a new volume from taking over the partition of another one.
  partitionType = label: uuidOf "chalkos-partition-type:${label}";

  reservedRepartKeys = [
    "Type"
    "Label"
    "Encrypt"
    "Format"
    "SizeMinBytes"
    "SizeMaxBytes"
    "MountPoint"
  ];
  # Labels the system region and VAR use. A volume on the system disk with one of them would
  # share its link below /dev/disk/chalk-boot with a partition of the image.
  reservedNames = [
    "esp"
    "store"
    "store-verity"
    "state"
    "var"
  ];
  forbiddenMountPoints = [
    "/"
    "/nix"
    "/state"
    "/var"
  ];

  validName =
    name:
    builtins.match "[a-z0-9]([a-z0-9-]*[a-z0-9])?" name != null && builtins.stringLength name <= 32;
  validMountPoint =
    path:
    # Mount points go verbatim into unit files, where spaces, newlines or backslashes would
    # change their meaning.
    builtins.match "(/[A-Za-z0-9._-]+)+" path != null
    && !lib.elem "." (lib.splitString "/" path)
    && !lib.elem ".." (lib.splitString "/" path)
    && !lib.elem path forbiddenMountPoints
    && !lib.hasPrefix "/nix/" path
    && !lib.hasPrefix "/state/" path;

  modeOf = storage: mode: if mode != null then mode else storage.encryption.mode;

  definition =
    {
      label,
      format,
      size,
      mode,
      repart ? { },
    }:
    lib.generators.toINI { listsAsDuplicateKeys = true; } {
      Partition =
        repart
        // {
          Type = partitionType label;
          Label = label;
        }
        // lib.optionalAttrs (format != null) { Format = format; }
        // lib.optionalAttrs (size != null) {
          SizeMinBytes = size;
          SizeMaxBytes = size;
        }
        // lib.optionalAttrs (mode == "tpm2") {
          Encrypt = "tpm2";
          TPM2PCRs = "7";
        };
    };

  volumeErrors =
    storage: name: v:
    let
      at = "volumes.${name}";
      checks = [
        {
          failed = !validName name;
          message = "${at}: the name must match [a-z0-9]([a-z0-9-]*[a-z0-9])? and have at most 32 characters";
        }
        {
          failed = lib.elem name reservedNames;
          message = "${at}: the name ${name} is reserved";
        }
        {
          failed = v.mountPoint != null && !validMountPoint v.mountPoint;
          message = "${at}.mountPoint must be an absolute path of the characters A-Z, a-z, 0-9, ., _, - and /, other than /, /nix, /state and /var, and not below /nix or /state";
        }
        {
          failed = v.format == null && v.mountPoint != null;
          message = "${at}: a raw volume (format = null) cannot have a mountPoint";
        }
        {
          failed = v.format == "swap" && v.mountPoint != null;
          message = "${at}: a swap volume cannot have a mountPoint";
        }
        {
          failed = v.disk != null && v.disk == storage.system.disk;
          message = "${at}.disk is the system disk; use disk = null to place the volume there";
        }
      ];
      reservedKeys = lib.intersectLists reservedRepartKeys (lib.attrNames v.repart);
    in
    map (c: c.message) (lib.filter (c: c.failed) checks)
    ++ map (key: "${at}.repart must not set ${key}; it comes from the typed options") reservedKeys;
in
{
  # GPT type UUID of the partition with the label. STATE, which the image defines, uses it too.
  inherit partitionType;

  # Evaluation errors in a node's storage options; empty when they are valid.
  errors =
    storage:
    let
      # A disabled volume is dropped before any check runs, as if the node never declared it.
      enabledVolumes = lib.filterAttrs (_: v: v.enable) storage.volumes;
      ownDisks = lib.filterAttrs (_: v: v.disk != null) enabledVolumes;
      sharedWith = name: v: lib.attrNames (lib.filterAttrs (o: w: o > name && w.disk == v.disk) ownDisks);
      sharedError =
        name: other:
        "volumes.${name} and volumes.${other} reference the same disk; a volume with a disk occupies it alone";
      # VAR counts as a volume of the system disk.
      filling =
        lib.optional (storage.var.size == null) "var"
        ++ lib.attrNames (lib.filterAttrs (_: v: v.disk == null && v.size == null) enabledVolumes);
      fillingError = "at most one volume per disk may have size = null; on the system disk these do: ${lib.concatStringsSep ", " filling}";
      # VAR is mounted at /var, so it takes part like any volume.
      mounts = [
        {
          name = "var";
          mountPoint = "/var";
        }
      ]
      ++ lib.mapAttrsToList (name: v: {
        name = "volumes.${name}";
        inherit (v) mountPoint;
      }) (lib.filterAttrs (_: v: v.mountPoint != null) enabledVolumes);
      mountErrors = lib.mapAttrsToList (
        path: ms: "${lib.concatMapStringsSep " and " (m: m.name) ms} have the same mountPoint ${path}"
      ) (lib.filterAttrs (_: ms: lib.length ms > 1) (lib.groupBy (m: m.mountPoint) mounts));
    in
    lib.concatLists (lib.mapAttrsToList (volumeErrors storage) enabledVolumes)
    ++ lib.concatLists (
      lib.mapAttrsToList (name: v: map (sharedError name) (sharedWith name v)) ownDisks
    )
    ++ lib.optional (lib.length filling > 1) fillingError
    ++ mountErrors;

  # The identity's storage section. On the system disk VAR comes first and volumes follow in
  # name order; a volume with its own disk names that disk.
  render =
    {
      cluster,
      node,
      storage,
    }:
    let
      # A disabled volume is dropped before rendering, so it has no disk, definition or entry.
      enabledVolumes = lib.filterAttrs (_: v: v.enable) storage.volumes;
      onSystem = lib.filterAttrs (_: v: v.disk == null) enabledVolumes;
      ownDisks = lib.filterAttrs (_: v: v.disk != null) enabledVolumes;
      varMode = modeOf storage storage.var.encryption.mode;
      definitionOf =
        name: v:
        definition {
          label = name;
          inherit (v) format size repart;
          mode = modeOf storage v.encryption.mode;
        };
      disk = name: ref: repart: {
        inherit ref repart;
        seed = uuidOf "chalkos-seed:${cluster}/${node}/${name}";
      };
    in
    {
      disks = {
        system = disk "system" storage.system.disk (
          {
            "50-var.conf" = definition {
              label = "var";
              format = "ext4";
              inherit (storage.var) size;
              mode = varMode;
            };
          }
          // lib.mapAttrs' (name: v: lib.nameValuePair "60-${name}.conf" (definitionOf name v)) onSystem
        );
      }
      // lib.mapAttrs (name: v: disk name v.disk { "10-${name}.conf" = definitionOf name v; }) ownDisks;
      volumes = {
        var = {
          disk = "system";
          label = "var";
          format = "ext4";
          mountPoint = "/var";
          encryption = varMode;
          inherit (storage.var) size;
        };
      }
      // lib.mapAttrs (name: v: {
        disk = if v.disk == null then "system" else name;
        label = name;
        inherit (v) format mountPoint size;
        encryption = modeOf storage v.encryption.mode;
      }) enabledVolumes;
      fallback = storage.encryption.fallback;
      # STATE follows the node's policy; Install creates it accordingly.
      encryption = storage.encryption.mode;
    };
}
