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
    # Each family's pod range, then the source ranges, from the node's Kubernetes document.
    rangesCommand = config.chalkos.kinds.Kubernetes.get {
      output = ''
        go-template={{$s := .spec}}{{range $s.ipFamilies}}pods {{index $s.podCIDRs .}}
        {{end}}{{range $s.vxlanSourceSubnets}}source {{.}}
        {{end}}'';
    };
  };

  # The reference plugins the cluster names (chalkos.cni.plugins), and with flannel its own plugin.
  # Copies, so the store holds these alone.
  cniPlugins =
    pkgs.runCommand "cni-plugins-chalkos-${pkgs.cni-plugins.version}"
      {
        plugins =
          map (name: "${pkgs.cni-plugins}/bin/${name}") (
            lib.sort lib.lessThan (lib.unique config.chalkos.cni.plugins)
          )
          ++ lib.optional flannel "${pkgs.cni-plugin-flannel}/bin/flannel";
      }
      ''
        mkdir -p $out/bin
        for plugin in $plugins; do
          cp $plugin $out/bin/
        done
      '';

  # The kubelet's configuration as chalkd writes it, for the test that compares the two.
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
    extraArgs = k.extraArgs;
    inherit (imageFile) images;
  };

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

  # containerd tries the mirrors in order, then the registry itself; docker.io's registry is
  # registry-1.docker.io.
  mirrorsTemplate = ''
    === {{.spec.registry}}
    server = "https://{{if eq .spec.registry "docker.io"}}registry-1.docker.io{{else}}{{.spec.registry}}{{end}}"
    {{range .spec.mirrors}}
    [host."{{.}}"]
      capabilities = ["pull", "resolve"]
    {{end}}'';
  registryMirrors = pkgs.callPackage ./registry-mirrors.nix {
    mirrorsCommand = config.chalkos.kinds.RegistryMirror.get {
      output = "go-template=${mirrorsTemplate}";
    };
  };

  # A packet mark bit of the control plane's ports, which chalkos-controlplane sets; kube-proxy
  # (0x4000, 0x8000), flannel and the VXLAN rule leave it alone. It is cleared on every packet
  # the node receives, so a network plugin of the cluster's own (cni.provider "none") must not
  # use it.
  controlPlaneMark = "0x02000000";
  controlPlanePorts = pkgs.callPackage ./controlplane-ports.nix {
    typeCommand = config.chalkos.kinds.Node.get { path = "spec.type"; };
    ports = config.networking.firewall.enable;
    mark = controlPlaneMark;
  };
in
{
  config = lib.mkMerge [
    # Apart from the rest: the condition reads chalkos, so it can guard values below chalkos but
    # not a definition of chalkos as a whole.
    {
      chalkos.consumers = lib.mkIf (kind != null) {
        chalkos-registry-mirrors.kinds = [ "RegistryMirror" ];
        chalkos-controlplane.kinds = [ "Node" ];
      };
    }
    (lib.mkIf (kind != null) {
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

      system.build.chalkosCNIPlugins = cniPlugins;
      # The objects and the kubelet configuration as the cluster definition renders them, which
      # chalkd renders from the node's config; tests compare the two.
      system.build.chalkosAddons = k.addons;
      system.build.chalkosKubeletConfig = kubeletConfig;
      # The scripts of the units that read the config, which checks run with documents of their
      # own.
      system.build.chalkosVxlanRule = vxlanRule;

      # containerd runs the CNI plugins, and portmap programs nftables with nft.
      systemd.services.containerd.path = [ pkgs.nftables ];

      environment.systemPackages = lib.mkIf config.chalkos.debug.tools [ pkgs.cri-tools ];

      virtualisation.containerd = {
        enable = true;
        settings = lib.mkForce {
          version = 4;
          plugins."io.containerd.cri.v1.images" = {
            # The RegistryMirror documents' hosts.toml files, which containerd reads on every pull.
            registry.config_path = "/run/containerd/certs.d";
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
        "chalkos/kubernetes/image.json".text = builtins.toJSON imageFile;
      };

      systemd.services.chalkos-registry-mirrors = {
        description = "Write containerd's registry mirrors from the config";
        wantedBy = [ "containerd.service" ];
        before = [ "containerd.service" ];
        after = [ "chalkos-identity.service" ];
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = lib.getExe registryMirrors;
        };
      };

      systemd.tmpfiles.rules = [
        "d /var/lib/cni/net.d 0755 root root -"
      ];

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
          # chalkd's preparation writes the configuration and the flags, the cluster's extra flags
          # of the kubelet last.
          ExecStart = lib.concatStringsSep " " [
            "${kubelet}/bin/kubelet"
            "--config=${run}/kubelet/config.json"
            "--kubeconfig=${run}/kubelet/kubeconfig"
            "--cert-dir=/var/lib/kubelet/pki"
            "$KUBELET_ARGS"
          ];
          Restart = "always";
          RestartSec = 10;
        };
        startLimitIntervalSec = 0;
      };

      systemd.services.chalkos-controlplane = {
        description = "Open the control plane's ports and make etcd's directory by the node's type";
        wantedBy = [ "multi-user.target" ];
        after = [
          "chalkos-identity.service"
          "local-fs.target"
        ]
        ++ lib.optional config.networking.firewall.enable "nftables.service";
        unitConfig.RequiresMountsFor = "/var/lib/etcd";
        before = [
          "chalkos-kubernetes.service"
          "kubelet.service"
        ];
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = lib.getExe controlPlanePorts;
        };
      };

      networking.firewall = {
        allowedTCPPorts = [ 10250 ];
        allowedTCPPortRanges = [
          {
            from = 30000;
            to = 32767;
          }
        ];
        # Accept what chalkos-controlplane and chalkos-vxlan marked. Their tables clear the marks
        # after this chain, so neither these packets nor those they carry keep them. etcd's
        # clients and peers on the other control-plane nodes need 2379 and 2380; etcd accepts only
        # certificates of its CA there. Known limitation: the ports are open to every source, not
        # only to the cluster's node addresses.
        extraInputRules = lib.concatStringsSep "\n" (
          [
            "tcp dport { 6443, 2379, 2380 } meta mark & ${controlPlaneMark} == ${controlPlaneMark} accept"
          ]
          ++ lib.optional flannel "udp dport 8472 meta mark & ${vxlanMark} == ${vxlanMark} accept"
        );
      };
    })
  ];
}
