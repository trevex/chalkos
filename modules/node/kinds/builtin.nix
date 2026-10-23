# The kinds every image accepts: the node's host name, secrets, OCI registries with their
# credentials and mirrors, storage, time and, on images with Kubernetes, the cluster's Kubernetes
# settings with its API server's endpoint and manifests and the node's type and its kubelet's
# settings. chalkd translates them into the node's identity and its Kubernetes cluster.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  kubernetes = config.chalkos.kubernetes.enable;
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

  # What a cluster was set up with stays once the node holds a Kubernetes share: its
  # certificates, etcd and the ranges its control plane allocated were made for it. The node
  # holds one Cluster document, compared with the running config's whatever its name, in the
  # families both configs have; ipFamilies, compared as a whole, refuses a change of the families.
  fixed = "they are fixed once the cluster exists, so keep them, or reinstall the node into a new cluster";
  # Binds was and gets to the running and the delivered Cluster spec around expr.
  bindSpecs =
    expr:
    "cel.bind(was, oldDocuments.map(o, oldDocuments[o].spec)[0], cel.bind(gets, documents.map(n, documents[n].spec)[0], ${expr}))";
  # A rule over one per-family setting, whose value in a spec s and family f is value "s" "f",
  # compared with ==, and shown as show gives it.
  fixedRule = field: value: show: {
    rule = "facts.share == null || size(oldDocuments) == 0 || size(documents) == 0 || ${bindSpecs "was.ipFamilies.filter(f, f in gets.ipFamilies).all(f, ${value "was" "f"} == ${value "gets" "f"})"}";
    messageExpression = bindSpecs ''
      cel.bind(families, was.ipFamilies.filter(f, f in gets.ipFamilies),
        "the config changes the cluster's ${field} from "
        + families.map(f, f + " " + ${show (value "was" "f")}).join(", ") + " to "
        + families.map(f, f + " " + ${show (value "gets" "f")}).join(", ") + "; ${fixed}")
    '';
  };
  # The storage kinds' rules compare the delivered documents with the section STATE records,
  # facts.recorded, rather than with the running config: the machine has one storage, which a
  # slot's config may grow but not undo. Null facts.recorded, as at an install, has nothing to
  # compare with. A recorded value is "" or null where the documents say null; a raw volume's
  # format is recorded as "".
  storageAPI = "storage.chalkos.dev/v1alpha1";
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
  # What a change of encryption, size or format needs a volume to be recreated for, "" for none.
  # A larger size, and filling the disk instead of a size, only grow the partition; sizes of the
  # same bytes are the same. A size bytes() cannot read, as a recorded section an older image wrote
  # can hold, is a change the volume is recreated for.
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
  # A rule over each Volume document n: none may be bad, and message names each that is.
  volumeRule = takes: bad: message: {
    inherit takes;
    rule = "facts.recorded == null || documents.all(n, ${bindVolume "!(${bad})"})";
    messageExpression = ''documents.filter(n, ${bindVolume bad}).map(n, ${bindVolume message}).join("; ")'';
  };
  reinstall = ''"; only a reinstall of " + node.name + " carries it out"'';
  # The new volumes that get the fallback keyslot, encrypted with TPM2 on a node with a fallback.
  enrolled = "documents.filter(n, !(n in facts.recorded.volumes) && ${bindVolume ''${volEncryption} == "tpm2"''})";

  # The cluster DNS's address: the one given, or the 10th of the family's service range.
  dnsIP =
    s: f: "(${s}.dnsIPs[${f}] != null ? ip(${s}.dnsIPs[${f}]) : cidr(${s}.serviceCIDRs[${f}]).ip(10))";
in
{
  # The Kubernetes share the node holds, null until it has one: rules compare the node's type with
  # its kind and keep the cluster's settings while there is one, and the kubelet restarts when it
  # changes. The fact keeps the kind and the hashes of the share's other keys, so no rule or
  # message sees its certificates and private keys, and every key a share gains counts.
  chalkos.facts = lib.mkIf kubernetes {
    share = {
      file = "/state/kubernetes/share.json";
      fields = [ "kind" ];
      hashOthers = true;
    };
  };

  chalkos.kinds = {
    Host = builtin "chalkos.dev" // {
      spec.hostname = mkOption {
        type = types.nullOr (types.strMatching "[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?");
        default = null;
        description = "Host name set at boot; null takes the node's name in the inventory.";
      };
    };
    Secret = builtin "chalkos.dev" // {
      multiple = true;
      spec.data = mkOption {
        type = types.attrsOf types.str;
        description = "Secret values by key, which secretRef fields of other documents name.";
      };
    };
    Registry = builtin "oci.chalkos.dev" // {
      multiple = true;
      # chalkd authenticates a pull with the Registry of the image's host, so a host has one; it
      # sends a user and a password together, so a Registry gives both or neither.
      rules = [
        {
          rule = "documents.all(n, documents.all(m, n == m || documents[n].spec.host != documents[m].spec.host))";
          messageExpression = ''"the Registry documents " + documents.filter(n, documents.exists(m, m != n && documents[m].spec.host == documents[n].spec.host)).join(", ") + " name the same host; give one Registry per host"'';
        }
        {
          rule = "documents.all(n, (documents[n].spec.username == null) == (documents[n].spec.password == null))";
          messageExpression = ''documents.filter(n, (documents[n].spec.username == null) != (documents[n].spec.password == null)).map(n, "Registry " + n).join(", ") + ": give username and password together, or neither to pull anonymously"'';
        }
      ];
      spec = {
        host = mkOption {
          type = types.strMatching "${host}(:[0-9]+)?";
          example = "ghcr.io";
          description = "Host of a registry or of a mirror's endpoint, as image references and endpoints name it, with its port unless it is 443; an IPv6 address in brackets. chalkd pulls OS images from this host with the Registry's credentials and leave for plain HTTP, and the kubelet gives containerd its credentials for the container images of this host.";
        };
        username = mkOption {
          type = types.nullOr types.nonEmptyStr;
          default = null;
          description = "User chalkd and containerd authenticate to the host as, with the password; null pulls anonymously.";
        };
        password = mkOption {
          type = types.nullOr lib.chalkos.types.secretRef;
          default = null;
          description = "Password or token of the user, from a Secret; null pulls anonymously.";
        };
        insecure = mkOption {
          type = types.bool;
          default = false;
          description = "Allow plain HTTP to this host: chalkd and containerd reach it over HTTP, and a Mirror may name an http:// endpoint on it. Otherwise chalkd pulls over HTTPS alone, also from private and loopback addresses. Credentials and images then travel unencrypted, so use it only on a trusted network, such as a lab.";
        };
      };
    };
    Mirror = builtin "oci.chalkos.dev" // {
      multiple = true;
      # chalkd and containerd try one list of endpoints for a registry, so a registry has one.
      rules = [
        {
          rule = "documents.all(n, documents.all(m, n == m || documents[n].spec.registry != documents[m].spec.registry))";
          messageExpression = ''"the Mirror documents " + documents.filter(n, documents.exists(m, m != n && documents[m].spec.registry == documents[n].spec.registry)).join(", ") + " mirror the same registry; give one Mirror per registry"'';
        }
      ];
      spec = {
        registry = mkOption {
          type = types.strMatching "${host}(:[0-9]+)?";
          example = "docker.io";
          description = "Host of the registry as image references name it, with its port unless it is 443; an IPv6 address in brackets.";
        };
        endpoints = mkOption {
          type = types.nonEmptyListOf (types.strMatching "https?://${host}(:[0-9]+)?(/[A-Za-z0-9._~%/-]*)?");
          example = [ "https://mirror.example.com" ];
          description = "URLs of the mirror's endpoints, which chalkd and containerd try in order before the registry itself. A path is where the endpoint serves the registry API, below <path>/v2 as containerd takes it. chalkd authenticates to an endpoint with the Registry of the endpoint's host, and an http:// endpoint needs that Registry to set insecure.";
        };
      };
    };
    SystemDisk = builtin "storage.chalkos.dev" // {
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
            description = "Overrides the node's encryption mode for VAR.";
          };
        };
      };
    };
    Volume = builtin "storage.chalkos.dev" // {
      multiple = true;
      # chalkctl storage reset recreates a volume in place, on the disk it lives on; moving a volume
      # to another disk, by its disk or by a reference that finds another disk, and removing it,
      # take a reinstall. A disk reference that cannot be resolved is a mistake in the config, or a
      # missing disk when it finds none, which neither carries out.
      rules = [
        (volumeRule "reinstall" "r != null && ${volDisk} != r.disk"
          ''"Volume " + n + ": disk " + ${volDisk} + " instead of " + r.disk + ${reinstall}''
        )
        (volumeRule "reinstall" movedByRef
          ''"Volume " + n + ": disk reference " + facts.disks[n].ref + " resolves to " + facts.disks[n].found + ", but the volume lives on " + facts.disks[n].identity + ${reinstall}''
        )
        (volumeRule "refuse" unresolved ''
          "Volume " + n + ": disk reference " + facts.disks[n].ref + " cannot be resolved: " + facts.disks[n].error + "; "
          + (facts.disks[n].errorKind == "noDisk" ? "fix the disk reference or plug in the disk it names"
            : facts.disks[n].errorKind == "listDisks" ? "try again once the node can list its disks"
            : "fix the disk reference")
        '')
        (volumeRule "reset" needsReset
          ''"Volume " + n + ": " + ${resetReasons}.join("; ") + "; reset it with chalkctl storage reset " + node.name + " " + n''
        )
        (volumeRule "refuse" missingDisk ''
          facts.disks[n].presentError != ""
            ? "Volume " + n + ": the node cannot find its disk " + n + ": " + facts.disks[n].presentError + "; try again once it can"
            : "Volume " + n + ": its disk " + n + " (" + facts.disks[n].identity + ") is not plugged in; plug it in"
        '')
        # A volume the running image's config never named was added by another image's config,
        # or an earlier one of the image a slot is written for, which the running slot is not.
        {
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
    Node = builtin "kubernetes.chalkos.dev" // {
      # The share's certificates are those of its kind, and a control plane's etcd data stays its
      # own, until the node is reinstalled; a share of another type delivered with the config
      # changes neither.
      rules = [
        {
          rule = "facts.share == null || documents.all(n, documents[n].spec.type == facts.share.kind)";
          messageExpression = ''
            cel.bind(type, documents.map(n, documents[n].spec.type)[0],
              "the config changes the node's type from \"" + facts.share.kind + "\" to \"" + type
              + "\"; a node keeps its type once it holds a Kubernetes share, so keep it, or reinstall the node as a " + type)
          '';
        }
      ];
      spec = {
        type = mkOption {
          type = types.enum [
            "controlplane"
            "worker"
          ];
          description = "What the node is in the cluster; it is fixed once the node holds a Kubernetes share.";
        };
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
        kubeletExtraArgs = flags "the kubelet";
        nodeIP = {
          addresses = mkOption {
            type = types.nullOr (types.listOf types.nonEmptyStr);
            default = null;
            description = "Fixed addresses of the node, at most one per family; null takes the first static address of each family unless validSubnets apply.";
          };
          validSubnets = mkOption {
            type = types.nullOr (types.listOf types.nonEmptyStr);
            default = null;
            description = "Subnets the node picks its address from, which replace the Cluster's nodeIP.validSubnets as a whole; null takes the Cluster's.";
          };
        };
        vip.interface = mkOption {
          type = types.nullOr types.nonEmptyStr;
          default = null;
          description = "Interface the node adds the Cluster's VIP addresses to while it holds them; null means the one holding the node's address of their family. Only control planes hold the VIP, so a worker's stays null.";
        };
      };
    };
    Cluster = builtin "kubernetes.chalkos.dev" // {
      rules = [
        (fixedRule "podCIDRs" (s: f: "cidr(${s}.podCIDRs[${f}])") (v: "string(${v})"))
        (fixedRule "serviceCIDRs" (s: f: "cidr(${s}.serviceCIDRs[${f}])") (v: "string(${v})"))
        (fixedRule "dnsIPs" dnsIP (v: "string(${v})"))
      ];
      spec = {
        endpoint = mkOption {
          type = types.strMatching "https://.+";
          description = "URL of the cluster's Kubernetes API server, which every node of the cluster names alike.";
        };
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
        ipFamilies = lib.chalkos.mkOption {
          type = types.nonEmptyListOf (
            types.enum [
              "ipv4"
              "ipv6"
            ]
          );
          default = [ "ipv4" ];
          description = "Address families of the cluster, the primary one first, each once. They are fixed when the cluster is created.";
          rules = [
            {
              rule = "facts.share == null || self == oldSelf";
              messageExpression = ''"the config changes the cluster's ipFamilies from " + oldSelf.join(", ") + " to " + self.join(", ") + "; ${fixed}"'';
            }
          ];
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
            description = "Subnets in CIDR notation that nodes without a fixed address pick theirs from at boot, unless a Node gives its own; a leading `!` excludes one. Empty takes any global unicast address.";
          };
          timeout = mkOption {
            type = types.ints.positive;
            default = 300;
            description = "Seconds a node waits at boot for its address.";
          };
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
        };
        extraArgs = {
          etcd = flags "etcd";
          kube-apiserver = flags "the API server";
          kube-controller-manager = flags "the controller-manager";
          kube-scheduler = flags "the scheduler";
        };
      };
    };
    Addon = builtin "kubernetes.chalkos.dev" // {
      multiple = true;
      spec.objects = mkOption {
        type = types.listOf kubernetesObject;
        description = "Kubernetes objects the control planes apply with server-side apply after chalkos's own, in this order; the objects of several Addon documents in the order of their names. chalkctl delivers them to control planes alone, and a worker applies none.";
      };
    };
  }
  // lib.optionalAttrs (kubernetes && flannel) {
    Flannel = builtin "kubernetes.chalkos.dev" // {
      spec = {
        mtu = mkOption {
          type = types.nullOr types.ints.positive;
          default = null;
          description = "MTU flannel's VXLAN assumes for the network between the nodes; its VXLAN devices and the pods get 50 less. null takes the MTU of the interface holding the node's address.";
        };
        vxlanSourceSubnets = mkOption {
          type = types.listOf types.nonEmptyStr;
          default = [ ];
          description = "Ranges flannel's VXLAN must come from, one of each family at least; empty takes VXLAN from any source.";
        };
      };
    };
  };
}
