# The kinds every image accepts: the cluster and node a config belongs to, the image the node
# runs, secrets, storage, time and, on images with Kubernetes, the cluster's Kubernetes settings,
# registry mirrors and manifests and the kubelet's node settings. chalkd translates them into the
# node's identity and its Kubernetes cluster.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  kubernetes = config.chalkos.role.kubernetes.enable;
  flannel = config.chalkos.cni.provider == "flannel";
  # A host as registries are named: a DNS name, an IPv4 address or an IPv6 address in brackets.
  # The closing bracket stays unescaped: POSIX ERE has no \], and a lone ] is literal in ERE,
  # ECMA-262 and Go alike.
  host = "([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?|\\[[0-9A-Fa-f:.]+])";
  inherit ((import ./schema.nix { inherit lib; }).types) kubernetesObject;
  # An option per address family, as the cluster's ranges are given.
  perFamily =
    {
      type,
      description,
      ipv4,
      ipv6,
    }:
    {
      ipv4 = mkOption {
        inherit type description;
        default = ipv4;
      };
      ipv6 = mkOption {
        inherit type description;
        default = ipv6;
      };
    };
  flags =
    component:
    mkOption {
      type = types.attrsOf types.str;
      default = { };
      description = "Extra flags of ${component}, named without the leading dashes with letters, digits, `_`, `.` and `-`, starting with a letter or digit; they override chalkos's own, except the flags chalkos protects, which are refused.";
    };

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
      spec = {
        servers = mkOption {
          type = types.listOf timeServer;
          default = map (n: { host = "ptbtime${toString n}.ptb.de"; }) [
            1
            2
            3
          ];
          description = "Time servers the node synchronises its clock with, authenticated with NTS unless `nts = false`.";
        };
        dhcp = mkOption {
          type = types.bool;
          default = false;
          description = "Also synchronise with the time servers the network announces through DHCP, which are not authenticated.";
        };
      };
    };
  }
  // lib.optionalAttrs kubernetes {
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
    Kubernetes = builtin "kubernetes.chalkos.dev" // {
      spec = {
        domain = mkOption {
          type = types.nonEmptyStr;
          default = "cluster.local";
          description = "DNS domain of the cluster.";
        };
        allowSchedulingOnControlPlanes = mkOption {
          type = types.bool;
          default = false;
          description = "Let workloads run on control-plane nodes, which are otherwise tainted.";
        };
        ipFamilies = mkOption {
          type = types.nonEmptyListOf (
            types.enum [
              "ipv4"
              "ipv6"
            ]
          );
          default = [ "ipv4" ];
          description = "Address families of the cluster, the primary one first, each once. They are fixed when the cluster is created.";
        };
        podCIDRs = perFamily {
          type = types.nonEmptyStr;
          description = "Address range of the family's pods; each node gets a part of it.";
          ipv4 = "10.244.0.0/16";
          ipv6 = "fd00:10:244::/56";
        };
        serviceCIDRs = perFamily {
          type = types.nonEmptyStr;
          description = "Address range of the family's services, at most 2^20 addresses.";
          ipv4 = "10.96.0.0/12";
          ipv6 = "fd00:10:96::/112";
        };
        dnsIPs = perFamily {
          type = types.nullOr types.nonEmptyStr;
          description = "Service address of the cluster DNS in the family's service range; null is its 10th address. Pods use the primary family's.";
          ipv4 = null;
          ipv6 = null;
        };
        nodeCIDRMaskSizes = perFamily {
          type = types.ints.between 1 128;
          description = "Prefix length of each node's part of the family's pod range.";
          ipv4 = 24;
          ipv6 = 64;
        };
        nodeIP = {
          validSubnets = mkOption {
            type = types.listOf types.nonEmptyStr;
            default = [ ];
            description = "Subnets in CIDR notation that nodes without a fixed address pick theirs from at boot; a leading `!` excludes one. Empty takes any global unicast address.";
          };
          timeout = mkOption {
            type = types.ints.positive;
            default = 300;
            description = "Seconds a node waits at boot for its address.";
          };
        };
        vxlanSourceSubnets = mkOption {
          type = types.listOf types.nonEmptyStr;
          default = [ ];
          description = "Ranges flannel's VXLAN must come from, one of each family at least; empty takes VXLAN from any source.";
        };
        vip = {
          addresses = mkOption {
            type = types.listOf types.nonEmptyStr;
            default = [ ];
            description = "Virtual addresses of the API server, at most one per family; empty means no virtual IP.";
          };
          mode = mkOption {
            type = types.enum [ "l2" ];
            default = "l2";
            description = "How the holder announces the addresses.";
          };
          interface = mkOption {
            type = types.nullOr types.nonEmptyStr;
            default = null;
            description = "Interface the addresses are added to; null means the one holding the node's address of their family.";
          };
        };
        extraArgs = {
          etcd = flags "etcd";
          kube-apiserver = flags "the API server";
          kube-controller-manager = flags "the controller-manager";
          kube-scheduler = flags "the scheduler";
          kubelet = flags "the kubelet";
        };
      };
    };
    RegistryMirror = builtin "kubernetes.chalkos.dev" // {
      multiple = true;
      spec = {
        registry = mkOption {
          type = types.strMatching "${host}(:[0-9]+)?";
          example = "docker.io";
          description = "Host of the registry as image references name it, with its port unless it is 443; an IPv6 address in brackets.";
        };
        mirrors = mkOption {
          type = types.nonEmptyListOf (
            types.strMatching "(https?://)?${host}(:[0-9]+)?(/[A-Za-z0-9._~:/?#@!$&'()*+,;=%-]*)?"
          );
          example = [ "https://mirror.example.com" ];
          description = "Mirrors of the registry, which containerd tries in order before the registry itself: URLs, or hosts with an optional port, which containerd reaches with HTTPS.";
        };
        allowPlainHTTP = mkOption {
          type = types.bool;
          default = false;
          description = "Allow mirrors that are not https:// URLs. It exists for test registries: nothing authenticates what a plain HTTP mirror serves.";
        };
      };
    };
    Manifest = builtin "kubernetes.chalkos.dev" // {
      multiple = true;
      spec.objects = mkOption {
        type = types.listOf kubernetesObject;
        description = "Kubernetes objects the control planes apply with server-side apply after chalkos's own, in this order; the objects of several Manifest documents in the order of their names.";
      };
    };
  }
  // lib.optionalAttrs (kubernetes && flannel) {
    Flannel = builtin "kubernetes.chalkos.dev" // {
      spec.mtu = mkOption {
        type = types.nullOr types.ints.positive;
        default = null;
        description = "MTU flannel's VXLAN assumes for the network between the nodes; its VXLAN devices and the pods get 50 less. null takes the MTU of the interface holding the node's address.";
      };
    };
  };
}
