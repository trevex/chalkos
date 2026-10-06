# Storage options of nodes and roles. A role's storage values become defaults of its nodes leaf by
# leaf, so a node overrides single attributes and adds volumes without restating the role's.
{ config, lib, ... }:
let
  inherit (lib) mkOption types;

  size = types.strMatching "[0-9]+[KMGTP]?" // {
    description = "size such as 512M or 2T (base 1024)";
  };
  mode = types.enum [
    "tpm2"
    "none"
  ];
  selectorKeys = [
    "model"
    "serial"
    "wwn"
    "size"
    "type"
  ];
  validSelector =
    s:
    s != { }
    && lib.all (k: lib.elem k selectorKeys) (lib.attrNames s)
    && (s ? size -> builtins.match "(<|<=|=|>=|>)? *[0-9]+[KMGTP]?" s.size != null)
    && (
      s ? type
      -> lib.elem s.type [
        "nvme"
        "ssd"
        "hdd"
      ]
    );
  diskRef =
    types.either (types.strMatching "/dev/.+") (types.addCheck (types.attrsOf types.str) validSelector)
    // {
      description = "disk reference (a /dev/ path, or a selector with model, serial, wwn, size or type)";
    };

  volume = {
    options = {
      enable = mkOption {
        type = types.bool;
        default = true;
        description = "Whether the node has this volume; set to false on a node to drop a volume its role defines.";
      };
      disk = mkOption {
        type = types.nullOr diskRef;
        default = null;
        example = {
          model = "Samsung SSD 990*";
          type = "nvme";
        };
        description = "Disk the volume occupies on its own; null places it on the system disk.";
      };
      size = mkOption {
        type = types.nullOr size;
        default = null;
        description = "Size of the volume; null fills the remaining space on its disk.";
      };
      format = mkOption {
        type = types.nullOr (
          types.enum [
            "ext4"
            "xfs"
            "btrfs"
            "swap"
          ]
        );
        default = "ext4";
        description = "File system of the volume; null leaves a raw block device.";
      };
      mountPoint = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "/var/lib/longhorn";
        description = "Where the volume is mounted; null leaves it unmounted.";
      };
      encryption.mode = mkOption {
        type = types.nullOr mode;
        default = null;
        description = "Overrides the node's encryption mode for this volume.";
      };
      repart = mkOption {
        type = types.attrsOf (
          types.oneOf [
            types.str
            types.int
            types.bool
            (types.listOf types.str)
          ]
        );
        default = { };
        example = {
          Weight = 2000;
        };
        description = ''
          Extra repart.d keys of the volume's partition, merged last. Type, Label, Encrypt,
          Format, SizeMinBytes, SizeMaxBytes and MountPoint come from the typed options.
        '';
      };
    };
  };

  storageOptions = forRole: {
    system.disk = mkOption (
      {
        type = if forRole then types.nullOr diskRef else diskRef;
        example = "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001";
        description = "Disk holding the system region and VAR.";
      }
      // lib.optionalAttrs forRole { default = null; }
    );
    encryption = {
      mode = mkOption {
        type = mode;
        default = "tpm2";
        description = ''
          Encryption of STATE, VAR and volumes: `tpm2` seals a LUKS2 key to PCR 7, `none` uses no
          encryption. STATE holds the node's secrets.
        '';
      };
      fallback = mkOption {
        type = types.enum [
          "recovery-key"
          "password"
          "none"
        ];
        default = "recovery-key";
        description = "Second keyslot asked for on the console when TPM2 unsealing fails.";
      };
    };
    var = {
      size = mkOption {
        type = types.nullOr size;
        default = null;
        description = "Size of VAR; null fills the remaining space on the system disk.";
      };
      encryption.mode = mkOption {
        type = types.nullOr mode;
        default = null;
        description = "Overrides the node's encryption mode for VAR.";
      };
    };
    volumes = mkOption {
      type = types.attrsOf (types.submodule volume);
      default = { };
      description = "Additional volumes, by name. The name is also the partition label.";
    };
  };

  # Every leaf of a role's storage becomes a default of the node. A volume's disk reference is a
  # leaf: a node replaces the role's reference instead of merging selector keys into it.
  isDiskRef =
    path: builtins.length path == 3 && builtins.head path == "volumes" && lib.last path == "disk";
  asDefaults =
    path: value:
    if builtins.isAttrs value && !(isDiskRef path) then
      lib.mapAttrs (name: asDefaults (path ++ [ name ])) value
    else
      lib.mkDefault value;

  roleDefaults =
    role:
    let
      s = config.chalkos.roles.${role}.storage;
    in
    asDefaults [ ] (removeAttrs s [ "system" ])
    // lib.optionalAttrs (s.system.disk != null) { system.disk = lib.mkDefault s.system.disk; };
in
{
  options.chalkos.roles = mkOption {
    type = types.attrsOf (
      types.submodule {
        options.storage = storageOptions true;
      }
    );
  };

  options.chalkos.nodes = mkOption {
    type = types.attrsOf (
      types.submodule (
        { config, ... }:
        {
          options.storage = storageOptions false;
          config.storage = roleDefaults config.role;
        }
      )
    );
  };

  config.chalkos.warnings = lib.concatLists (
    lib.mapAttrsToList (
      name: node:
      lib.optional (node.storage.encryption.mode == "none") ''
        chalkos.nodes.${name}.storage.encryption.mode is "none": STATE, which holds the node's
        secrets, is not encrypted.
      ''
    ) config.chalkos.nodes
  );
}
