# The node's part in the Kubernetes cluster and the cluster's settings, on images with
# Kubernetes: the node's type, labels and taints, the cluster's endpoint, ranges and component
# flags, the Addon manifests and, with flannel as the CNI, flannel's settings.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin;
  kubernetes = config.chalkos.kubernetes.enable;
  flannel = config.chalkos.cni.provider == "flannel";
  inherit ((import ../schema.nix { inherit lib; }).types) kubernetesObject;
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
        description = "Effect of the taint on pods that do not tolerate it.";
      };
    };
  };
  # What a cluster was set up with stays once the node holds a Kubernetes share: its
  # certificates, etcd and the ranges its control plane allocated were made for it. The node
  # holds one Cluster document, compared with the running config's whatever its name, in the
  # families both configs have; ipFamilies, compared as a whole, refuses a change of the families.
  fixed = "they are fixed once the cluster exists, so keep them, or reinstall the node into a new cluster";
  # The kubelet applies the node's labels and taints, the control-plane taint among them, when it
  # registers the node with the cluster, and chalkd never changes the Node object after that. A
  # node holding a Kubernetes share has registered, or registers with the config it holds.
  registered = description: what: instead: {
    inherit description;
    rule = "facts.share == null || self == oldSelf";
    message = "the config changes ${what}; the value applies when the node registers with the cluster, so changing it on a running node changes nothing; ${instead}, or reinstall the node";
  };
  # Binds was and gets to the running and the delivered Cluster spec around expr.
  bindSpecs =
    expr:
    "cel.bind(was, oldDocuments.map(o, oldDocuments[o].spec)[0], cel.bind(gets, documents.map(n, documents[n].spec)[0], ${expr}))";
  # A rule over one per-family setting, whose value in a spec s and family f is value "s" "f",
  # compared with ==, and shown as show gives it.
  fixedRule = description: field: value: show: {
    inherit description;
    rule = "facts.share == null || size(oldDocuments) == 0 || size(documents) == 0 || ${bindSpecs "was.ipFamilies.filter(f, f in gets.ipFamilies).all(f, ${value "was" "f"} == ${value "gets" "f"})"}";
    messageExpression = bindSpecs ''
      cel.bind(families, was.ipFamilies.filter(f, f in gets.ipFamilies),
        "the config changes the cluster's ${field} from "
        + families.map(f, f + " " + ${show (value "was" "f")}).join(", ") + " to "
        + families.map(f, f + " " + ${show (value "gets" "f")}).join(", ") + "; ${fixed}")
    '';
  };

  # The cluster DNS's address: the one given, or the 10th of the family's service range.
  dnsIP =
    s: f: "(${s}.dnsIPs[${f}] != null ? ip(${s}.dnsIPs[${f}]) : cidr(${s}.serviceCIDRs[${f}]).ip(10))";
in
{
  config = lib.mkIf kubernetes {
    # The Kubernetes share the node holds, null until it has one: rules compare the node's type with
    # its kind and keep the cluster's settings while there is one, and the kubelet restarts when it
    # changes. The fact keeps the kind and the hashes of the share's other keys, so no rule or
    # message sees its certificates and private keys, and every key a share gains counts.
    chalkos.facts.share = {
      file = "/state/kubernetes/share.json";
      fields = [ "kind" ];
      hashOthers = true;
    };

    chalkos.kinds = {
      Node = builtin "kubernetes.chalkos.dev" // {
        description = "The node's part in the Kubernetes cluster: its type, the labels and taints it registers with, its address, the interface of the VIP and its kubelet's flags.";
        # The share's certificates are those of its kind, and a control plane's etcd data stays its
        # own, until the node is reinstalled; a share of another type delivered with the config
        # changes neither.
        rules = [
          {
            description = "The node keeps the type its Kubernetes share is for.";
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
            description = "Type of the node in the cluster; it is fixed once the node holds a Kubernetes share.";
          };
          labels = lib.chalkos.mkOption {
            type = types.attrsOf types.str;
            default = { };
            description = "Kubernetes labels the node registers with; they are fixed once the node holds a Kubernetes share.";
            rules = [
              (registered "The node keeps its labels once it holds a Kubernetes share." "the node's labels"
                "change the Node object with kubectl label instead"
              )
            ];
          };
          taints = lib.chalkos.mkOption {
            type = types.listOf taint;
            default = [ ];
            description = "Kubernetes taints the node registers with; they are fixed once the node holds a Kubernetes share.";
            rules = [
              (registered "The node keeps its taints once it holds a Kubernetes share." "the node's taints"
                "change the Node object with kubectl taint instead"
              )
            ];
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
        description = "The Kubernetes cluster the node belongs to: its API server's endpoint, its address families and ranges, its VIP and the flags of its control-plane components.";
        rules = [
          (fixedRule
            "The pod range of each family both configs have stays once the node holds a Kubernetes share."
            "podCIDRs"
            (s: f: "cidr(${s}.podCIDRs[${f}])")
            (v: "string(${v})")
          )
          (fixedRule
            "The service range of each family both configs have stays once the node holds a Kubernetes share."
            "serviceCIDRs"
            (s: f: "cidr(${s}.serviceCIDRs[${f}])")
            (v: "string(${v})")
          )
          (fixedRule
            "The cluster DNS's address of each family both configs have, by default the 10th of its service range, stays once the node holds a Kubernetes share."
            "dnsIPs"
            dnsIP
            (v: "string(${v})")
          )
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
          allowSchedulingOnControlPlanes = lib.chalkos.mkOption {
            type = types.bool;
            default = false;
            description = "Whether workloads run on control-plane nodes, which otherwise register with the taint `node-role.kubernetes.io/control-plane:NoSchedule`; it is fixed once the node holds a Kubernetes share.";
            rules = [
              (registered "Whether workloads run on control planes stays once the node holds a Kubernetes share."
                "allowSchedulingOnControlPlanes"
                "add or remove the node-role.kubernetes.io/control-plane:NoSchedule taint with kubectl taint instead"
              )
            ];
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
                description = "The cluster's address families stay once the node holds a Kubernetes share.";
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
              description = "Way the node holding the VIP announces its addresses: `l2` adds them to an interface and announces them to the neighbours with gratuitous ARP and unsolicited neighbour advertisements.";
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
        description = "Kubernetes objects that the control planes apply to the cluster.";
        spec.objects = mkOption {
          type = types.listOf kubernetesObject;
          description = "Kubernetes objects the control planes apply with server-side apply after chalkos's own, in this order; the objects of several Addon documents in the order of their names. chalkctl delivers them to control planes alone, and a worker applies none.";
        };
      };
      Flannel = lib.mkIf flannel (
        builtin "kubernetes.chalkos.dev"
        // {
          description = "The settings of flannel, the cluster's CNI, for the VXLAN between the nodes.";
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
        }
      );
    };
  };
}
