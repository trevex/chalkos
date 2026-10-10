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
  primary = builtins.head k.ipFamilies;
  # The entries of the families in use.
  ofFamilies = byFamily: lib.genAttrs k.ipFamilies (family: byFamily.${family});
  controlPlane = kind == "controlplane";
  flannel = config.chalkos.cni.provider == "flannel";

  # flannel's VXLAN carries pod traffic unauthenticated, so it is accepted only when sent to the
  # node's addresses, which flanneld uses and other nodes send to: on the interface holding one,
  # or on any for one on a loopback or dummy interface. chalkd picks them after the firewall
  # started, so a table of chalkos's own marks such packets, filled by this script from the file
  # the preparation writes and emptied without one, and the firewall accepts what carries the
  # mark. The firewall's reloads leave that table alone.
  vxlanFirewall = flannel && config.networking.firewall.enable;
  # A packet mark bit kube-proxy (0x4000, 0x8000) and flannel leave alone.
  vxlanMark = "0x01000000";
  vxlanRule = pkgs.callPackage ./vxlan-rule.nix {
    ipv6 = config.networking.enableIPv6;
    mark = vxlanMark;
    podCIDRs = lib.attrValues (ofFamilies k.podCIDRs);
    sourceSubnets = k.vxlanSourceSubnets;
  };

  # The plugins flannel's network runs: flannel delegates to bridge with host-local addresses,
  # and portmap serves host ports; containerd sets up each pod's loopback with loopback. Copies,
  # so the store holds these alone.
  cniPlugins =
    pkgs.runCommand "cni-plugins-${pkgs.cni-plugins.version}"
      {
        plugins =
          map (name: "${pkgs.cni-plugins}/bin/${name}") [
            "bridge"
            "host-local"
            "loopback"
            "portmap"
          ]
          ++ lib.optional flannel "${pkgs.cni-plugin-flannel}/bin/flannel";
      }
      ''
        mkdir -p $out/bin
        for plugin in $plugins; do
          cp $plugin $out/bin/
        done
      '';

  kubeletConfig = {
    apiVersion = "kubelet.config.k8s.io/v1beta1";
    kind = "KubeletConfiguration";
    authentication = {
      anonymous.enabled = false;
      webhook.enabled = true;
      x509.clientCAFile = "${run}/kubelet/ca.crt";
    };
    authorization.mode = "Webhook";
    readOnlyPort = 0;
    tlsMinVersion = "VersionTLS12";
    # The kubelet refuses to start unless the kernel tunables below hold its values, rather than
    # changing them itself.
    protectKernelDefaults = true;
    cgroupDriver = "systemd";
    clusterDNS = [ k.dnsIPs.${primary} ];
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
    inherit (k) domain allowSchedulingOnControlPlanes;
    podCIDRs = ofFamilies k.podCIDRs;
    serviceCIDRs = ofFamilies k.serviceCIDRs;
    dnsIPs = ofFamilies k.dnsIPs;
    nodeCIDRMaskSizes = ofFamilies k.nodeCIDRMaskSizes;
    version = k.package.version;
    inherit (k) ipFamilies;
    nodeIP = { inherit (k.nodeIP) validSubnets timeout; };
    inherit (k) vxlanSourceSubnets;
    # The nodes check that their addresses suit flannel.
    flannel = if flannel then { mtu = lib.defaultTo 0 config.chalkos.cni.flannel.mtu; } else null;
    vip = { inherit (k.vip) addresses mode interface; };
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
      # What the kubelet's protectKernelDefaults checks.
      "vm.overcommit_memory" = 1;
      "vm.panic_on_oom" = 0;
      "kernel.panic" = 10;
      "kernel.panic_on_oops" = 1;
      "kernel.keys.root_maxkeys" = 1000000;
      "kernel.keys.root_maxbytes" = 25000000;
    };

    # containerd and its runc shim alone: ctr and containerd-stress are half of the package. A copy,
    # so the store holds these two alone.
    nixpkgs.overlays = [
      (_: prev: {
        containerd =
          prev.runCommand "containerd-${prev.containerd.version}"
            {
              pname = "containerd";
              inherit (prev.containerd) version;
              # The copy has out alone; containerd's meta names man too, for the system path.
              meta = prev.containerd.meta // {
                outputsToInstall = [ "out" ];
              };
              binaries = [
                "containerd"
                "containerd-shim-runc-v2"
              ];
            }
            ''
              mkdir -p $out/bin
              for binary in $binaries; do
                cp ${prev.containerd}/bin/$binary $out/bin/
              done
            '';
      })
    ];

    # containerd runs the CNI plugins, and portmap programs nftables with nft.
    systemd.services.containerd.path = [ pkgs.nftables ];

    environment.systemPackages = lib.mkIf config.chalkos.debug.tools [ pkgs.cri-tools ];

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
            bin_dirs = [ "${cniPlugins}/bin" ];
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

    # The node's addresses have settled. A unit that configures or announces addresses, such as a
    # routing daemon's, orders itself before the target and is wanted by it; the preparation picks
    # the node's addresses after it, and still waits up to nodeIP.timeout for them.
    systemd.targets.chalkos-node-addresses = {
      description = "The node's addresses have settled";
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
    };

    # Certificates, kubeconfigs and kubelet flags from the node's share and identity.
    systemd.services.chalkos-kubernetes = {
      description = "Prepare the node's Kubernetes certificates and configuration";
      wantedBy = [ "multi-user.target" ];
      wants = [ "chalkos-node-addresses.target" ];
      after = [
        "chalkos-identity.service"
        "local-fs.target"
        "chalkos-node-addresses.target"
      ]
      ++ lib.optional vxlanFirewall "nftables.service";
      before = [ "kubelet.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe chalkd} prepare-kubernetes";
      }
      // lib.optionalAttrs vxlanFirewall {
        # No VXLAN is accepted while the addresses are picked, nor when none are found.
        ExecStartPre = [ (lib.getExe vxlanRule) ];
        # The preparation fills the table last, so the node is marked prepared only with the rules
        # in place, and a failure to fill it is the preparation's, which chalkd reports.
        ExecStart = "${lib.getExe chalkd} prepare-kubernetes ${lib.getExe vxlanRule}";
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
      # etcd's clients and peers on the other control-plane nodes need 2379 and 2380; etcd accepts
      # only certificates of its CA there. Known limitation: the ports are open to every source,
      # not only to the cluster's node addresses.
      allowedTCPPorts = [
        10250
      ]
      ++ lib.optionals controlPlane [
        6443
        2379
        2380
      ];
      allowedTCPPortRanges = [
        {
          from = 30000;
          to = 32767;
        }
      ];
      # Accept the VXLAN packets chalkos-vxlan marked. The table clears the mark after this chain,
      # so neither these packets nor those they carry keep it.
      extraInputRules = lib.mkIf flannel ''
        udp dport 8472 meta mark & ${vxlanMark} == ${vxlanMark} accept
      '';
    };
  };
}
