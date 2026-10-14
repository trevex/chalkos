# The kinds every image accepts: the cluster and node a config belongs to, the image the node
# runs, secrets, storage, time and, on images with Kubernetes, the kubelet's node settings. chalkd
# translates them into the node's identity.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  kubernetesKind = config.chalkos.role.kubernetes.kind;

  size = types.strMatching "[0-9]+[KMGTP]?";
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
        type = types.nullOr (types.strMatching "(<|<=|=|>=|>)? *[0-9]+[KMGTP]?");
        default = null;
        description = "Comparison the disk's size meets, such as `>= 1T`.";
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
  timeServer = types.submodule {
    options = {
      host = mkOption {
        type = types.strMatching "[A-Za-z0-9][A-Za-z0-9.:-]*";
        description = "Host name or address of the time server.";
      };
      nts = mkOption {
        type = types.bool;
        default = true;
        description = "Authenticate the server with Network Time Security (NTS).";
      };
      port = mkOption {
        type = types.nullOr types.port;
        default = null;
        description = "UDP port of the server's NTP; null is NTP's port, 123.";
      };
    };
  };
  taint = types.submodule {
    options = {
      key = mkOption {
        type = types.nonEmptyStr;
        description = "Taint key.";
      };
      value = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Taint value; null for a taint without a value.";
      };
      effect = mkOption {
        type = types.enum [
          "NoSchedule"
          "PreferNoSchedule"
          "NoExecute"
        ];
        description = "What happens to pods that do not tolerate the taint.";
      };
    };
  };
  builtin = group: {
    inherit group;
    version = "v1alpha1";
    builtin = true;
  };
in
{
  chalkos.kinds = {
    Cluster = builtin "chalkos.dev" // {
      spec.endpoint = mkOption {
        type = types.nullOr (types.strMatching "https://.+");
        default = null;
        description = "URL of the cluster's Kubernetes API server.";
      };
    };
    Node = builtin "chalkos.dev" // {
      spec = {
        type = mkOption {
          type = types.nullOr (
            types.enum [
              "controlplane"
              "worker"
            ]
          );
          default = null;
          description = "What the node is in Kubernetes; null on an image without Kubernetes.";
        };
        imageRef = mkOption {
          type = types.nonEmptyStr;
          description = "Name of the Image document of the image the node runs.";
        };
        hostname = mkOption {
          type = types.nullOr (types.strMatching "[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?");
          default = null;
          description = "Host name set at boot; null uses the node's name.";
        };
      };
    };
    Image = builtin "chalkos.dev" // {
      multiple = true;
      spec = {
        source = mkOption {
          type = types.nonEmptyStr;
          description = "Where the image is: a directory of an image build.";
        };
        rootHash = mkOption {
          type = types.nullOr (types.strMatching "[0-9a-f]{64}");
          default = null;
          description = "Root hash of the image's store, which the source must have; null accepts any.";
        };
      };
    };
    Secret = builtin "chalkos.dev" // {
      multiple = true;
      spec.data = mkOption {
        type = types.attrsOf types.str;
        description = "Secret values by key, which secretRef fields of other documents name.";
      };
    };
    SystemDisk = builtin "storage.chalkos.dev" // {
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
            description = "Size of VAR; null fills the remaining space on the system disk.";
          };
          encryption.mode = mkOption {
            type = types.nullOr mode;
            default = null;
            description = "Overrides the node's encryption mode for VAR.";
          };
        };
      };
    };
    Volume = builtin "storage.chalkos.dev" // {
      multiple = true;
      spec = {
        disk = mkOption {
          type = types.nullOr diskRef;
          default = null;
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
          description = "Where the volume is mounted; null leaves it unmounted.";
        };
        encryption.mode = mkOption {
          type = types.nullOr mode;
          default = null;
          description = "Overrides the node's encryption mode for this volume.";
        };
        repart = mkOption {
          type = types.attrsOf types.str;
          default = { };
          description = "Extra repart.d keys of the volume's partition. Type, Label, Encrypt, Format, SizeMinBytes, SizeMaxBytes and MountPoint come from the typed options.";
        };
      };
    };
    TimeSync = builtin "time.chalkos.dev" // {
      spec.servers = mkOption {
        type = types.listOf timeServer;
        default = map (n: { host = "ptbtime${toString n}.ptb.de"; }) [
          1
          2
          3
        ];
        description = "Time servers the node synchronises its clock with, authenticated with NTS unless `nts = false`.";
      };
    };
  }
  // lib.optionalAttrs (kubernetesKind != null) {
    Kubelet = builtin "kubernetes.chalkos.dev" // {
      spec = {
        labels = mkOption {
          type = types.attrsOf types.str;
          default = { };
          description = "Kubernetes labels applied to the node.";
        };
        taints = mkOption {
          type = types.listOf taint;
          default = [ ];
          description = "Kubernetes taints applied to the node.";
        };
        nodeIPs = mkOption {
          type = types.nullOr (types.listOf types.nonEmptyStr);
          default = null;
          description = "Fixed addresses of the node, at most one per family; null takes the first static address of each family unless validSubnets apply.";
        };
        validSubnets = mkOption {
          type = types.nullOr (types.listOf types.nonEmptyStr);
          default = null;
          description = "Subnets the node picks its address from instead of the cluster's; null uses those.";
        };
      };
    };
  };
}
