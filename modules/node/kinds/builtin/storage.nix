# The node's storage: the system disk with the encryption of the node's storage, and the volumes.
# The facts the rules read, facts.recorded, facts.disks and facts.fallback, are declared in
# base/storage.nix with the chalkos-storage command that reads them.
{ lib, ... }:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin groups apiVersions;
  # A size as repart reads it, in powers of 1024: at most 18 digits without a suffix and 15, 12, 9,
  # 6 or 3 with K, M, G, T or P, so it stays below 2^63 bytes, which a rule reads as an int.
  sizePattern = "[0-9]{1,18}|[0-9]{1,15}K|[0-9]{1,12}M|[0-9]{1,9}G|[0-9]{1,6}T|[0-9]{1,3}P";
  size = types.strMatching sizePattern;
  sizeLimit = "at most 18 digits without a suffix, and 15, 12, 9, 6 or 3 with K, M, G, T or P";
  mode = types.enum [
    "tpm2"
    "none"
  ];
  diskRef = types.submodule {
    options = {
      path = mkOption {
        type = types.nullOr (types.strMatching "/dev/.+");
        default = null;
        description = "Path of the disk below /dev, such as /dev/disk/by-id/…; excludes the selector keys.";
      };
      model = mkOption {
        type = types.nullOr types.nonEmptyStr;
        default = null;
        description = "Glob the disk's model matches, such as `Samsung SSD 870*`.";
      };
      serial = mkOption {
        type = types.nullOr types.nonEmptyStr;
        default = null;
        description = "The disk's serial number.";
      };
      wwn = mkOption {
        type = types.nullOr types.nonEmptyStr;
        default = null;
        description = "The disk's World Wide Name.";
      };
      size = mkOption {
        type = types.nullOr (types.strMatching "(<|<=|=|>=|>)? *(${sizePattern})");
        default = null;
        description = "Comparison the disk's size meets, such as `>= 1T`; the size has ${sizeLimit}.";
      };
      type = mkOption {
        type = types.nullOr (
          types.enum [
            "nvme"
            "ssd"
            "hdd"
          ]
        );
        default = null;
        description = "Kind of disk.";
      };
    };
  };
  # The storage kinds' rules compare the delivered documents with the section STATE records,
  # facts.recorded, rather than with the running config: the machine has one storage, which a
  # slot's config may grow but not undo. Null facts.recorded, as at an install, has nothing to
  # compare with. A recorded value is "" or null where the documents say null; a raw volume's
  # format is recorded as "".
  storageAPI = apiVersions.storage;
  systemDiskSpec = ''config["${storageAPI}"].SystemDisk.map(k, config["${storageAPI}"].SystemDisk[k].spec)[0]'';
  orEmpty = x: ''(${x} == null ? "" : ${x})'';
  # A recorded volume's format as the documents write it: an empty one is a raw volume's.
  shown = x: ''(${x} == "" ? "raw" : ${x})'';
  # Binds v to volume n's spec, sd to the SystemDisk's and r to the volume's recorded definition,
  # null without one, around expr. The rules read facts.disks only for a volume with a disk of its
  # own, so a node whose volumes all live on the system disk runs no command for it.
  bindVolume =
    expr:
    "cel.bind(v, documents[n].spec, cel.bind(sd, ${systemDiskSpec}, cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, ${expr})))";
  # The volume's definition as the recorded section writes it.
  volDisk = ''(v.disk == null ? "system" : n)'';
  # raw is recorded empty, as it was when null meant raw, so a raw volume's recorded format does
  # not read as another.
  volFormat = ''(v.format == "raw" ? "" : v.format)'';
  volEncryption = "(v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode)";
  # The reason a change from the recorded size needs the volume recreated, or "" if it does not.
  # - A larger size, or filling the disk instead of a size, only grows the partition.
  # - Sizes of the same bytes, such as 1G and 1024M, are the same size.
  # - A size bytes() cannot read, which a section an older image recorded can hold, needs the
  #   volume recreated.
  sizeReason =
    size: recorded:
    ''(${size} == ${recorded} || ${size} == "" ? "" : ${recorded} == "" ? "size " + ${size} + " instead of filling its disk" : !isSize(${size}) || !isSize(${recorded}) ? "size " + ${size} + " instead of " + ${recorded} : bytes(${size}) < bytes(${recorded}) ? "smaller: " + ${size} + " instead of " + ${recorded} : "")'';
  # Whether two sizes differ, as sizes of the same bytes do not.
  sizeChanged =
    a: b: "(${a} != ${b} && !(isSize(${a}) && isSize(${b}) && bytes(${a}) == bytes(${b})))";
  encryptionReason =
    enc: recorded:
    ''(${enc} != ${recorded} ? "encryption " + ${enc} + " instead of " + ${recorded} : "")'';
  resetReasons = ''
    [${volFormat} != ${orEmpty "r.format"} ? "format " + ${shown volFormat} + " instead of " + ${shown (orEmpty "r.format")} : "",
     ${encryptionReason volEncryption "r.encryption"},
     ${sizeReason (orEmpty "v.size") (orEmpty "r.size")}].filter(x, x != "")'';
  ownDisk = "v.disk != null && n in facts.disks";
  movedByRef = ''${ownDisk} && facts.disks[n].changed && facts.disks[n].error == "" && !facts.disks[n].same'';
  unresolved = ''${ownDisk} && facts.disks[n].changed && facts.disks[n].error != ""'';
  needsReset = "r != null && ${volDisk} == r.disk && !(${movedByRef}) && !(${unresolved}) && size(${resetReasons}) > 0";
  volChanges = "r == null || ${volFormat} != ${orEmpty "r.format"} || ${volEncryption} != r.encryption || ${sizeChanged (orEmpty "v.size") (orEmpty "r.size")} || ${orEmpty "v.mountPoint"} != ${orEmpty "r.mountPoint"}";
  missingDisk = "${ownDisk} && facts.disks[n].pinned && !facts.disks[n].present && (${volChanges})";
  # A rule over each Volume document n: `bad` holds for none, and `message` describes each it
  # holds for. Both are CEL expressions over n and the names bindVolume binds.
  volumeRule =
    {
      takes,
      description,
      bad,
      message,
    }:
    {
      inherit takes description;
      rule = "facts.recorded == null || documents.all(n, ${bindVolume "!(${bad})"})";
      messageExpression = ''documents.filter(n, ${bindVolume bad}).map(n, ${bindVolume message}).join("; ")'';
    };
  reinstall = ''"; only a reinstall of " + node.name + " carries it out"'';
  # The new volumes that get the fallback keyslot, encrypted with TPM2 on a node with a fallback.
  enrolled = "documents.filter(n, !(n in facts.recorded.volumes) && ${bindVolume ''${volEncryption} == "tpm2"''})";
in
{
  chalkos.kinds = {
    SystemDisk = builtin groups.storage // {
      description = "The disk that holds the node's system region and VAR, and the encryption of the node's storage.";
      # VAR holds the running node's data, so a change it would be recreated for takes a
      # reinstall; a larger VAR, or one that fills the disk, only grows. VAR is never raw, so a
      # recorded VAR without a format shows as none.
      rules =
        let
          bindVar =
            expr:
            ''cel.bind(sd, documents.map(n, documents[n].spec)[0], cel.bind(r, facts.recorded.volumes["var"], ${expr}))'';
          reasons = ''
            [${orEmpty "r.format"} != "ext4" ? "format ext4 instead of " + (${orEmpty "r.format"} == "" ? "none" : r.format) : "",
             ${encryptionReason "(sd.var.encryption.mode != null ? sd.var.encryption.mode : sd.encryption.mode)" "r.encryption"},
             ${sizeReason (orEmpty "sd.var.size") (orEmpty "r.size")}].filter(x, x != "")'';
        in
        [
          {
            description = "VAR keeps its format, encryption and size; a larger VAR, or one that fills the disk, is accepted.";
            takes = "reinstall";
            rule = ''facts.recorded == null || !("var" in facts.recorded.volumes) || size(documents) != 1 || ${bindVar "size(${reasons}) == 0"}'';
            messageExpression = bindVar ''"VAR: " + ${reasons}.join("; ") + ${reinstall}'';
          }
        ];
      spec = {
        disk = mkOption {
          type = diskRef;
          description = "Disk holding the system region and VAR.";
        };
        encryption = {
          mode = mkOption {
            type = mode;
            default = "tpm2";
            description = "Encryption of STATE, VAR and volumes: `tpm2` seals a LUKS2 key to PCR 7, `none` uses no encryption.";
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
            description = "Size of VAR, such as `20G`, with ${sizeLimit}; null fills the remaining space on the system disk.";
          };
          encryption.mode = mkOption {
            type = types.nullOr mode;
            default = null;
            description = "Encryption mode of VAR, over the node's; null takes the node's.";
          };
        };
      };
    };
    Volume = builtin groups.storage // {
      multiple = true;
      description = "A partition for the node's data, on the system disk or on a disk of its own, which the node creates and mounts.";
      # chalkctl storage reset recreates a volume in place, on the disk it lives on; moving a volume
      # to another disk, by its disk or by a reference that finds another disk, and removing it,
      # take a reinstall. A disk reference that cannot be resolved is a mistake in the config, or a
      # missing disk when it finds none, which neither carries out.
      rules = [
        (volumeRule {
          takes = "reinstall";
          description = "A volume stays on the disk it was created on: the system disk or a disk of its own.";
          bad = "r != null && ${volDisk} != r.disk";
          message = ''"Volume " + n + ": disk " + ${volDisk} + " instead of " + r.disk + ${reinstall}'';
        })
        (volumeRule {
          takes = "reinstall";
          description = "A disk reference that differs from the one the volume's disk was pinned by finds that disk.";
          bad = movedByRef;
          message = ''"Volume " + n + ": disk reference " + facts.disks[n].ref + " resolves to " + facts.disks[n].found + ", but the volume lives on " + facts.disks[n].identity + ${reinstall}'';
        })
        (volumeRule {
          takes = "refuse";
          description = "A disk reference that differs from the one the volume's disk was pinned by resolves.";
          bad = unresolved;
          message = ''
            "Volume " + n + ": disk reference " + facts.disks[n].ref + " cannot be resolved: " + facts.disks[n].error + "; "
            + (facts.disks[n].errorKind == "noDisk" ? "fix the disk reference or plug in the disk it names"
              : facts.disks[n].errorKind == "listDisks" ? "try again once the node can list its disks"
              : "fix the disk reference")
          '';
        })
        (volumeRule {
          takes = "reset";
          description = "A volume keeps its format and encryption, also the encryption it inherits from the SystemDisk, and does not shrink or take a fixed size where it filled its disk.";
          bad = needsReset;
          message = ''"Volume " + n + ": " + ${resetReasons}.join("; ") + "; reset it with chalkctl storage reset " + node.name + " " + n'';
        })
        (volumeRule {
          takes = "refuse";
          description = "The pinned disk of a volume that the config creates or changes is plugged in.";
          bad = missingDisk;
          message = ''
            facts.disks[n].presentError != ""
              ? "Volume " + n + ": the node cannot find its disk " + n + ": " + facts.disks[n].presentError + "; try again once it can"
              : "Volume " + n + ": its disk " + n + " (" + facts.disks[n].identity + ") is not plugged in; plug it in"
          '';
        })
        # A volume the running image's config never named was added by another image's config,
        # or an earlier one of the image a slot is written for, which the running slot is not.
        {
          description = "No volume the machine records is left out of the config.";
          takes = "reinstall";
          rule = ''facts.recorded == null || facts.recorded.volumes.all(n, n == "var" || n in documents)'';
          messageExpression = ''
            facts.recorded.volumes.filter(n, n != "var" && !(n in documents)).map(n, n in oldDocuments
              ? "Volume " + n + ": the config removes it; only a reinstall of " + node.name + " removes a volume"
              : "Volume " + n + ": an earlier config of this image or another added it, with an upgrade or chalkctl apply-config --staged, and it stays on the node; name it in this config too; only a reinstall of " + node.name + " removes it").join("; ")
          '';
        }
        # New encrypted volumes are enrolled with the fallback secret, which must open the node's
        # existing fallback keyslot, so a mistyped one cannot leave volumes whose fallbacks differ.
        {
          description = "A new TPM2-encrypted volume, on a node with a fallback, comes with the fallback secret, which opens the node's fallback keyslot.";
          rule = ''facts.recorded == null || ${systemDiskSpec}.encryption.fallback == "none" || size(${enrolled}) == 0 || (facts.fallback.sent && facts.fallback.matches != false)'';
          messageExpression = ''
            !facts.fallback.sent
              ? "Volume " + ${enrolled}[0] + " is encrypted and the node's fallback is " + ${systemDiskSpec}.encryption.fallback + ", but no fallback secret was sent"
              : "the fallback secret does not match the node's existing fallback keyslot"
          '';
        }
      ];
      spec = {
        disk = mkOption {
          type = types.nullOr diskRef;
          default = null;
          description = "Disk the volume occupies on its own; null places it on the system disk.";
        };
        size = mkOption {
          type = types.nullOr size;
          default = null;
          description = "Size of the volume, such as `100G`, with ${sizeLimit}; null fills the remaining space on its disk.";
        };
        format = mkOption {
          type = types.enum [
            "ext4"
            "xfs"
            "btrfs"
            "swap"
            "raw"
          ];
          default = "ext4";
          description = "File system of the volume; `raw` leaves a raw block device, which has no mountPoint.";
        };
        mountPoint = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Where the volume is mounted; null leaves it unmounted.";
        };
        encryption.mode = mkOption {
          type = types.nullOr mode;
          default = null;
          description = "Encryption mode of the volume, over the node's; null takes the node's.";
        };
        repart = mkOption {
          type = types.attrsOf types.str;
          default = { };
          description = "Extra repart.d keys of the volume's partition. Type, Label, Encrypt, Format, SizeMinBytes, SizeMaxBytes and MountPoint come from the typed options.";
        };
      };
    };
  };
}
