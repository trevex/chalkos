# Kubernetes on nodes of a role with a kind: containerd and the kubelet from the image, and on
# control-plane nodes the static pods chalkd renders. chalkd writes everything node-specific
# below /run/chalkos/kubernetes before the kubelet starts.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (config.chalkos.role.kubernetes) kind;
  inherit (config.chalkos) cluster;
  k = cluster.kubernetes;
  chalkd = pkgs.callPackage ../../nix/chalkd.nix { };
  # The kubelet alone: the package holds every component, which control planes run from images.
  kubelet = pkgs.runCommand "kubelet-${k.package.version}" { } ''
    install -Dm755 ${k.package}/bin/kubelet $out/bin/kubelet
  '';
  run = "/run/chalkos/kubernetes";
  controlPlane = kind == "controlplane";

  cniPlugins = [
    pkgs.cni-plugins
  ]
  ++ lib.optional (config.chalkos.cni.provider == "flannel") pkgs.cni-plugin-flannel;

  kubeletConfig = {
    apiVersion = "kubelet.config.k8s.io/v1beta1";
    kind = "KubeletConfiguration";
    authentication = {
      anonymous.enabled = false;
      webhook.enabled = true;
      x509.clientCAFile = "${run}/kubelet/ca.crt";
    };
    authorization.mode = "Webhook";
    cgroupDriver = "systemd";
    clusterDNS = [ k.dnsIP ];
    clusterDomain = k.domain;
    containerRuntimeEndpoint = "unix:///run/containerd/containerd.sock";
    # The client certificate from chalkos is renewed by the kubelet; the serving certificate is
    # requested from the cluster and approved by chalkd on the control plane.
    rotateCertificates = true;
    serverTLSBootstrap = true;
    staticPodPath = "${run}/manifests";
    # The stub resolver at 127.0.0.53 is unreachable from pods.
    resolvConf = "/run/systemd/resolve/resolv.conf";
  };

  # Read by chalkd: what the static pods and certificates of this cluster need.
  clusterFile = {
    inherit kind;
    inherit (cluster) endpoint;
    inherit (k)
      podCIDR
      serviceCIDR
      dnsIP
      domain
      allowSchedulingOnControlPlanes
      ;
    version = k.package.version;
    extraArgs = removeAttrs k.extraArgs [ "kubelet" ];
    images = {
      inherit (k.images) etcd;
      kubeAPIServer = "registry.k8s.io/kube-apiserver:v${k.package.version}";
      kubeControllerManager = "registry.k8s.io/kube-controller-manager:v${k.package.version}";
      kubeScheduler = "registry.k8s.io/kube-scheduler:v${k.package.version}";
    };
  };

  upstreams."docker.io" = "https://registry-1.docker.io";
  # containerd tries the mirrors in order, then the registry itself.
  hostsToml =
    registry: mirrors:
    ''
      server = "${upstreams.${registry} or "https://${registry}"}"
    ''
    + lib.concatMapStrings (mirror: ''

      [host."${mirror}"]
        capabilities = ["pull", "resolve"]
    '') mirrors;
in
{
  config = lib.mkIf (kind != null) {
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
    };

    virtualisation.containerd = {
      enable = true;
      settings = lib.mkForce {
        version = 4;
        plugins."io.containerd.cri.v1.images" = {
          registry.config_path = "/etc/containerd/certs.d";
          pinned_images.sandbox = k.images.pause;
        };
        plugins."io.containerd.cri.v1.runtime" = {
          # /etc is read-only, so the pod network's configuration lives on VAR.
          cni = {
            bin_dirs = map (p: "${p}/bin") cniPlugins;
            conf_dir = "/var/lib/cni/net.d";
          };
          containerd.runtimes.runc.options.SystemdCgroup = true;
        };
      };
    };

    environment.etc = {
      "chalkos/kubernetes/cluster.json".text = builtins.toJSON clusterFile;
      "chalkos/kubernetes/kubelet.json".text = builtins.toJSON kubeletConfig;
    }
    // lib.optionalAttrs controlPlane {
      "chalkos/kubernetes/manifests.json".text = builtins.toJSON (k.addons ++ cluster.manifests);
    }
    // lib.mapAttrs' (
      registry: mirrors:
      lib.nameValuePair "containerd/certs.d/${registry}/hosts.toml" { text = hostsToml registry mirrors; }
    ) cluster.registries.mirrors;

    systemd.tmpfiles.rules = [
      "d /var/lib/cni/net.d 0755 root root -"
    ]
    ++ lib.optional controlPlane "d /var/lib/etcd 0700 root root -";

    # Certificates, kubeconfigs and kubelet flags from the node's share and identity.
    systemd.services.chalkos-kubernetes = {
      description = "Prepare the node's Kubernetes certificates and configuration";
      wantedBy = [ "multi-user.target" ];
      after = [
        "chalkos-identity.service"
        "local-fs.target"
      ];
      before = [ "kubelet.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe chalkd} prepare-kubernetes";
      };
    };

    systemd.services.kubelet = {
      description = "Kubernetes kubelet";
      wantedBy = [ "multi-user.target" ];
      wants = [
        "containerd.service"
        "network-online.target"
      ];
      requires = [ "chalkos-kubernetes.service" ];
      after = [
        "containerd.service"
        "chalkos-kubernetes.service"
        "network-online.target"
      ];
      # A node without a share has no kubeconfig and runs no kubelet.
      unitConfig.ConditionPathExists = "${run}/kubelet/kubeconfig";
      path = [
        pkgs.util-linux
        pkgs.iproute2
      ];
      serviceConfig = {
        EnvironmentFile = "${run}/kubelet/flags";
        ExecStart = lib.concatStringsSep " " (
          [
            "${kubelet}/bin/kubelet"
            "--config=/etc/chalkos/kubernetes/kubelet.json"
            "--kubeconfig=${run}/kubelet/kubeconfig"
            "--cert-dir=/var/lib/kubelet/pki"
            "$KUBELET_ARGS"
          ]
          ++ lib.mapAttrsToList (name: value: "--${name}=${value}") k.extraArgs.kubelet
        );
        Restart = "always";
        RestartSec = 10;
      };
      startLimitIntervalSec = 0;
    };

    networking.firewall = {
      allowedTCPPorts = [ 10250 ] ++ lib.optional controlPlane 6443;
      allowedTCPPortRanges = [
        {
          from = 30000;
          to = 32767;
        }
      ];
      # flannel's VXLAN.
      allowedUDPPorts = [ 8472 ];
    };
  };
}
