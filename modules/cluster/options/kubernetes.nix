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
    extraArgs = {
      etcd = flagsOf "etcd";
      kube-apiserver = flagsOf "kube-apiserver";
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

  options.chalkos.cluster.registries.mirrors = mkOption {
    type = types.attrsOf (types.listOf types.str);
    default = { };
    example = {
      "docker.io" = [ "https://mirror.example.com" ];
    };
    description = ''
      Mirrors of container registries, by registry host. containerd tries them in order and
      falls back to the registry itself.
    '';
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

  options.chalkos.nodes = mkOption {
    type = types.attrsOf (
      types.submodule (
        { config, ... }:
        {
          options.kubernetes.nodeIP = mkOption {
            type = types.nullOr types.str;
            default = firstStaticAddress config.network;
            defaultText = lib.literalMD "the node's first static address";
            description = ''
              Address the kubelet registers the node with. null lets the kubelet use the address
              of the default route.
            '';
          };
        }
      )
    );
  };
}
