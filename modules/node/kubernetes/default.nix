# Kubernetes on images that run it: containerd and the kubelet of the image's Kubernetes release,
# and the image's Kubernetes facts in /etc/chalkos/kubernetes/image.json. chalkd builds the node's
# cluster from them and the node's config, and writes everything node-specific below
# /run/chalkos/kubernetes before the kubelet starts.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  k = config.chalkos.kubernetes;
  inherit (k) enable;
  inherit (chalkosPackages) chalkd;
  # The kubelet alone: the package holds every component, which control planes run from images.
  kubelet = pkgs.runCommand "kubelet-${k.package.version}" { } ''
    install -Dm755 ${k.package}/bin/kubelet $out/bin/kubelet
  '';
  # Where chalkd writes the node's Kubernetes files; credprovider.Dir in Go is its
  # credential-provider directory.
  run = "/run/chalkos/kubernetes";
  flannel = config.chalkos.cni.provider == "flannel";

  # Read by chalkd with the node's config: what the image fixes about Kubernetes.
  imageFile = {
    version = k.package.version;
    cni = config.chalkos.cni.provider;
    images = {
      inherit (k.images) etcd coredns;
      kubeAPIServer = "registry.k8s.io/kube-apiserver:v${k.package.version}";
      kubeControllerManager = "registry.k8s.io/kube-controller-manager:v${k.package.version}";
      kubeScheduler = "registry.k8s.io/kube-scheduler:v${k.package.version}";
      kubeProxy = "registry.k8s.io/kube-proxy:v${k.package.version}";
      flannel = if flannel then config.chalkos.cni.flannel.image else "";
    };
  };
in
{
  imports = [
    ./effects.nix
    ./containerd.nix
    ./registries.nix
    ./firewall.nix
  ];

  options.chalkos.kubernetes = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Whether the image runs Kubernetes: containerd, the kubelet and the preparation of the
        node's Kubernetes configuration. One image serves control planes and workers: the type
        of the node's Kubernetes Node document makes it one or the other. mkImages sets it from
        the variant's `kubernetes`; the installer runs none.
      '';
    };
    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.kubernetes;
      defaultText = lib.literalExpression "pkgs.kubernetes";
      description = ''
        Kubernetes release of the image. Nodes run its kubelet; control planes run the
        upstream images of the same version. A Kubernetes upgrade is an image upgrade.
      '';
    };
    images = {
      etcd = lib.mkOption {
        type = lib.types.str;
        default = "registry.k8s.io/etcd:3.7.0-0";
        description = "Image of etcd. The default is the version kubeadm uses with the release of the default `chalkos.kubernetes.package`; an image with another release may need another.";
      };
      pause = lib.mkOption {
        type = lib.types.str;
        default = "registry.k8s.io/pause:3.10.2";
        description = "Image of the pod sandbox.";
      };
      coredns = lib.mkOption {
        type = lib.types.str;
        default = "registry.k8s.io/coredns/coredns:v1.14.6";
        description = "Image of the cluster DNS.";
      };
    };
  };

  config = lib.mkIf enable {
    # The cluster file chalkd writes, which the kubelet's rules compare.
    chalkos.facts.cluster.file = "${run}/cluster.json";

    boot.kernelModules = [
      "overlay"
      "br_netfilter"
      "vxlan"
    ];
    boot.kernel.sysctl = {
      "net.ipv4.ip_forward" = 1;
      "net.ipv6.conf.all.forwarding" = 1;
      "net.bridge.bridge-nf-call-iptables" = 1;
      "net.bridge.bridge-nf-call-ip6tables" = 1;
      # What the kubelet's protectKernelDefaults checks.
      "vm.overcommit_memory" = 1;
      "vm.panic_on_oom" = 0;
      "kernel.panic" = 10;
      "kernel.panic_on_oops" = 1;
      "kernel.keys.root_maxkeys" = 1000000;
      "kernel.keys.root_maxbytes" = 25000000;
    };

    # What the image fixes about Kubernetes, which its image directory's metadata.json names.
    system.build.chalkosKubernetesImage = imageFile;

    environment.etc = {
      "chalkos/kubernetes/image.json".text = builtins.toJSON imageFile;
    };

    # The node's addresses have settled. A unit that configures or announces addresses, such as a
    # routing daemon's, orders itself before the target and is wanted by it; the preparation picks
    # the node's addresses after it, and still waits up to nodeIP.timeout for them.
    systemd.targets.chalkos-node-addresses = {
      description = "The node's addresses have settled";
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
    };

    # Certificates, kubeconfigs and kubelet flags from the node's share and identity. With
    # flannel's VXLAN rule, firewall.nix has chalkd fill its table too.
    systemd.services.chalkos-kubernetes = {
      description = "Prepare the node's Kubernetes certificates and configuration";
      wantedBy = [ "multi-user.target" ];
      wants = [ "chalkos-node-addresses.target" ];
      after = [
        "chalkos-config.service"
        "local-fs.target"
        "chalkos-node-addresses.target"
      ];
      before = [ "kubelet.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.mkDefault "${lib.getExe chalkd} prepare-kubernetes";
      };
    };

    systemd.services.kubelet = {
      description = "Kubernetes kubelet";
      wantedBy = [ "multi-user.target" ];
      # Without the credential provider's files the kubelet pulls anonymously, so a failure to
      # write them leaves the node running pods.
      wants = [
        "containerd.service"
        "network-online.target"
        "chalkos-registry-credentials.service"
      ];
      requires = [ "chalkos-kubernetes.service" ];
      after = [
        "containerd.service"
        "chalkos-kubernetes.service"
        "chalkos-registry-credentials.service"
        "network-online.target"
      ];
      unitConfig = {
        # A node without a share has no kubeconfig and runs no kubelet.
        ConditionPathExists = "${run}/kubelet/kubeconfig";
        # A control plane's etcd pod would make its directory on the file system below a volume
        # that is missing, and start empty there.
        RequiresMountsFor = "/var/lib/etcd";
      };
      path = [
        pkgs.util-linux
        pkgs.iproute2
      ];
      serviceConfig = {
        EnvironmentFile = [
          "${run}/kubelet/flags"
          "-${run}/credential-provider/flags"
        ];
        # chalkd's preparation writes the configuration and the flags, the cluster's extra flags
        # of the kubelet last; chalkos-registry-credentials the credential provider's flags.
        ExecStart = lib.concatStringsSep " " [
          "${kubelet}/bin/kubelet"
          "--config=${run}/kubelet/config.json"
          "--kubeconfig=${run}/kubelet/kubeconfig"
          "--cert-dir=/var/lib/kubelet/pki"
          "$KUBELET_CREDENTIAL_PROVIDER_ARGS"
          "$KUBELET_ARGS"
        ];
        Restart = "always";
        RestartSec = 10;
      };
      startLimitIntervalSec = 0;
    };
  };
}
