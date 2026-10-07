# Kubernetes options of the cluster, its roles and its nodes.
{
  config,
  lib,
  nixpkgs,
  ...
}:
let
  inherit (lib) mkOption types;
  pkgs = nixpkgs.legacyPackages.${config.chalkos.cluster.system};
  flags = types.attrsOf types.str;
  flagsOf =
    component:
    mkOption {
      type = flags;
      default = { };
      description = "Extra flags of ${component}, without the leading dashes; they override chalkos's own.";
    };

  # The first address of the node's networks in network name order, without its prefix length.
  firstStaticAddress =
    network:
    let
      networks = network.networks or { };
      addresses = lib.concatMap (name: networks.${name}.address or [ ]) (lib.attrNames networks);
    in
    if addresses == [ ] then null else builtins.head (lib.splitString "/" (builtins.head addresses));

  # The endpoint's host when it is an IP literal, without brackets; null for a hostname.
  endpointIP =
    let
      authority = builtins.head (
        lib.splitString "/" (lib.removePrefix "https://" config.chalkos.cluster.endpoint)
      );
      ipv6 = builtins.match "\\[([^]]+)].*" authority;
      ipv4 = builtins.match "([0-9]{1,3}(\\.[0-9]{1,3}){3})(:.*)?" authority;
    in
    if ipv6 != null then
      builtins.head ipv6
    else if ipv4 != null then
      builtins.head ipv4
    else
      null;
  # The fixed addresses of the control-plane nodes; null for a node that picks its address at
  # boot.
  controlPlaneIPs = lib.mapAttrsToList (_: node: node.kubernetes.nodeIP) (
    lib.filterAttrs (
      _: node: config.chalkos.roles.${node.role}.kubernetes.kind == "controlplane"
    ) config.chalkos.nodes
  );

  # A subnet in CIDR notation, excluded with a leading "!"; chalkd checks the addresses.
  subnet = types.strMatching "!?[0-9A-Fa-f.:]+/[0-9]{1,3}";
  clusterSubnets = config.chalkos.cluster.kubernetes.nodeIP.validSubnets;
in
{
  options.chalkos.cluster.kubernetes = {
    package = mkOption {
      type = types.package;
      default = pkgs.kubernetes;
      defaultText = lib.literalExpression "pkgs.kubernetes";
      description = ''
        Kubernetes release of the cluster. Nodes run its kubelet; the control plane runs the
        upstream images of the same version. A Kubernetes upgrade is an image upgrade.
      '';
    };
    podCIDR = mkOption {
      type = types.str;
      default = "10.244.0.0/16";
      description = "Address range of pods; each node gets a part of it.";
    };
    serviceCIDR = mkOption {
      type = types.str;
      default = "10.96.0.0/12";
      description = "Address range of services.";
    };
    dnsIP = mkOption {
      type = types.str;
      default = "10.96.0.10";
      description = "Service address of the cluster DNS, inside serviceCIDR.";
    };
    domain = mkOption {
      type = types.str;
      default = "cluster.local";
      description = "DNS domain of the cluster.";
    };
    allowSchedulingOnControlPlanes = mkOption {
      type = types.bool;
      default = false;
      description = "Let workloads run on control-plane nodes, which are otherwise tainted.";
    };
    nodeIP = {
      validSubnets = mkOption {
        type = types.listOf subnet;
        default = [ ];
        example = [
          "10.0.0.0/8"
          "!10.0.0.10/32"
        ];
        description = ''
          Subnets in CIDR notation, IPv4 or IPv6, that nodes without a fixed nodeIP pick their
          address from at every boot; a leading `!` excludes a subnet. A node takes the first
          matching address: IPv4 before IPv6, then by interface name and address. Empty takes
          any global unicast address. Addresses in the pod and service ranges and on the
          interfaces of the pod network and kube-proxy (`flannel.*`, `cni*`, `veth*`, `kube-*`)
          are never taken. chalkos.nodes.<name>.kubernetes.validSubnets overrides this per node.
        '';
      };
      timeout = mkOption {
        type = types.ints.positive;
        default = 300;
        description = ''
          Seconds a node waits at boot for its address, so DHCP leases and addresses a routing
          daemon adds late still count. A node without one runs neither the kubelet nor static
          pods.
        '';
      };
    };
    extraArgs = {
      etcd = flagsOf "etcd";
      kube-apiserver = flagsOf "kube-apiserver" // {
        # These flags decide who may do what; chalkos sets them and chalkd refuses overrides.
        apply =
          flags:
          let
            protected = lib.intersectLists (lib.attrNames flags) [
              "anonymous-auth"
              "authentication-config"
              "authorization-mode"
              "enable-bootstrap-token-auth"
            ];
          in
          if protected == [ ] then
            flags
          else
            throw "chalkos.cluster.kubernetes.extraArgs.kube-apiserver cannot override ${lib.concatStringsSep ", " protected}: chalkos sets them";
      };
      kube-controller-manager = flagsOf "kube-controller-manager";
      kube-scheduler = flagsOf "kube-scheduler";
      kubelet = flagsOf "the kubelet";
    };
    images = {
      etcd = mkOption {
        type = types.str;
        default = "registry.k8s.io/etcd:3.7.0-0";
        description = "Image of etcd; the default is the version kubeadm uses with this Kubernetes release.";
      };
      pause = mkOption {
        type = types.str;
        default = "registry.k8s.io/pause:3.10.2";
        description = "Image of the pod sandbox.";
      };
      coredns = mkOption {
        type = types.str;
        default = "registry.k8s.io/coredns/coredns:v1.14.6";
        description = "Image of the cluster DNS.";
      };
    };
  };

  options.chalkos.cluster.manifests = mkOption {
    type = types.listOf (types.attrsOf types.anything);
    default = [ ];
    example = lib.literalExpression ''
      [ { apiVersion = "v1"; kind = "Namespace"; metadata.name = "apps"; } ]
    '';
    description = ''
      Kubernetes objects the control plane applies with server-side apply after the built-in
      ones, in this order, at bootstrap and at every boot.
    '';
  };

  options.chalkos.cluster.registries = {
    mirrors = mkOption {
      type = types.attrsOf (types.listOf types.str);
      default = { };
      example = {
        "docker.io" = [ "https://mirror.example.com" ];
      };
      # Nothing authenticates a plain HTTP mirror: anyone on the path can replace its images.
      apply =
        mirrors:
        let
          plain = lib.concatLists (
            lib.mapAttrsToList (
              registry: endpoints:
              map (endpoint: "${registry}: ${endpoint}") (
                lib.filter (endpoint: !lib.hasPrefix "https://" endpoint) endpoints
              )
            ) mirrors
          );
        in
        if plain == [ ] || config.chalkos.cluster.registries.allowPlainHTTP then
          mirrors
        else
          throw "chalkos.cluster.registries.mirrors must be https:// URLs unless chalkos.cluster.registries.allowPlainHTTP is set: ${lib.concatStringsSep ", " plain}";
      description = ''
        Mirrors of container registries, by registry host, as https:// URLs. containerd tries
        them in order and falls back to the registry itself.
      '';
    };
    allowPlainHTTP = mkOption {
      type = types.bool;
      default = false;
      description = ''
        Allow mirrors that are not https:// URLs. It exists for test registries: nothing
        authenticates what a plain HTTP mirror serves.
      '';
    };
  };

  options.chalkos.roles = mkOption {
    type = types.attrsOf (
      types.submodule {
        options.kubernetes.kind = mkOption {
          type = types.nullOr (
            types.enum [
              "controlplane"
              "worker"
            ]
          );
          default = "worker";
          description = ''
            What the role's nodes are in Kubernetes. Control-plane nodes run etcd and the control
            plane besides the kubelet; null leaves Kubernetes out of the role's image.
          '';
        };
      }
    );
  };

  # Hostnames are not checked: they may name a load balancer or a DNS record of the nodes. Nor
  # is the endpoint when a control-plane node picks its address at boot, which may be it.
  config.chalkos.warnings =
    lib.optional
      (
        controlPlaneIPs != [ ]
        && !lib.elem null controlPlaneIPs
        && endpointIP != null
        && !lib.elem endpointIP controlPlaneIPs
      )
      ''
        chalkos.cluster.endpoint ${config.chalkos.cluster.endpoint} is not the nodeIP of a
        control-plane node. The endpoint must reach a control-plane node, for example through a
        node's IP, a load balancer or a VIP.
      '';

  options.chalkos.nodes = mkOption {
    type = types.attrsOf (
      types.submodule (
        { config, ... }:
        let
          subnets =
            if config.kubernetes.validSubnets != null then config.kubernetes.validSubnets else clusterSubnets;
        in
        {
          options.kubernetes.nodeIP = mkOption {
            type = types.nullOr types.str;
            default = if subnets == [ ] then firstStaticAddress config.network else null;
            defaultText = lib.literalMD "the node's first static address, unless validSubnets apply to the node";
            description = ''
              Fixed address of the node: the kubelet registers it and, on control-plane nodes,
              etcd and the API server advertise it. The node waits at boot until an interface
              holds it. null picks the address on the node, from the validSubnets that apply.
            '';
          };
          options.kubernetes.validSubnets = mkOption {
            type = types.nullOr (types.listOf subnet);
            default = null;
            example = [ "192.168.100.0/24" ];
            description = ''
              Subnets the node picks its address from instead of
              chalkos.cluster.kubernetes.nodeIP.validSubnets; null uses those.
            '';
          };
        }
      )
    );
  };
}
