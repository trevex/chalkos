# Kubernetes on images that run it: containerd and the kubelet of the image's Kubernetes release,
# and the image's Kubernetes facts in /etc/chalkos/kubernetes/image.json. chalkd builds the node's
# cluster from them and the node's config, and writes everything node-specific below
# /run/chalkos/kubernetes before the kubelet starts.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  k = config.chalkos.kubernetes;
  inherit (k) enable;
  chalkd = pkgs.callPackage ../../nix/chalkd.nix { };
  # The kubelet alone: the package holds every component, which control planes run from images.
  kubelet = pkgs.runCommand "kubelet-${k.package.version}" { } ''
    install -Dm755 ${k.package}/bin/kubelet $out/bin/kubelet
  '';
  run = "/run/chalkos/kubernetes";
  flannel = config.chalkos.cni.provider == "flannel";

  # When the node's Kubernetes files change, which the preparation writes and the kubelet reads:
  # the node's address, the firewall's VXLAN rule, the kubelet's credentials and flags and the
  # control plane's certificates come from the share, the node's identity and its cluster.
  cel = import ./runtime/cel.nix { inherit lib; };
  core = "chalkos.dev/v1alpha1";
  kubernetesAPI = "kubernetes.chalkos.dev/v1alpha1";
  # The Address= values of each Network's [Network] sections, as written and in the order of the
  # networks' names, which the identity takes the node's static addresses from. They are compared
  # with their prefix lengths: taking those off loops over the addresses of each section of each
  # network, which costs more than a rule may.
  staticAddresses =
    c:
    "cel.bind(d, ${
      cel.documents c "networkd.chalkos.dev/v1alpha1" "Network"
    }, d.map(n, has(d[n].spec.network) ? ${cel.oneOrMany "d[n].spec.network"}.map(s, type(s) == map && has(s.address) ? s.address : null) : []))";
  # The cluster the Cluster and Flannel documents give, the endpoint among it, or a cluster file
  # that changed, as one a boot whose config loader failed left missing.
  clusterChanged = lib.concatStringsSep " || " [
    "facts.cluster != oldFacts.cluster"
    (cel.specsChanged kubernetesAPI "Cluster")
    (cel.specsChanged kubernetesAPI "Flannel")
  ];
  # A share that differs in more than its node CA: the node CA is read from the share whenever a
  # node certificate is issued, and nothing else uses it, so a new one restarts nothing. The share
  # fact keeps the hashes of its keys.
  withoutNodeCA = s: ''${s}.filter(k, k != "nodeCA").map(k, k + "=" + ${s}[k])'';
  shareChanged = "cel.bind(was, oldFacts.share, cel.bind(gets, facts.share, was == null || gets == null ? was != gets : ${withoutNodeCA "was"} != ${withoutNodeCA "gets"}))";
  # Only what the kubelet and its preparation read counts. chalkd once compared the whole identity
  # when it had no Kubernetes part, and restarted the kubelet for any other change of it, such as
  # of the storage or the time servers. That was a mistake, corrected here.
  kubernetesChanged = lib.concatStringsSep " || " [
    # The node's type, which is the cluster's kind, its labels, taints and addresses.
    (cel.specsChanged kubernetesAPI "Node")
    # The Host's host name, which the identity's Kubernetes node carries beside its name.
    (cel.specsChanged core "Host")
    "${staticAddresses "config"} != ${staticAddresses "oldConfig"}"
    clusterChanged
    shareChanged
  ];
  # A control plane applies the objects of changed Addon documents at once, as its loop does
  # whenever it starts. A worker applies none: chalkctl delivers none to it, and one it is given
  # anyway changes nothing it runs.
  addonObjects = c: "${cel.specs c kubernetesAPI "Addon"}.map(s, s.objects).flatten()";
  addonsApplied = ''(cel.bind(d, ${
    cel.documents "config" kubernetesAPI "Node"
  }, d.exists(n, d[n].spec.type == "controlplane")) && ${addonObjects "config"} != ${addonObjects "oldConfig"})'';

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
    # Each family's pod range, then the source ranges, from the node's Cluster document.
    rangesCommand = config.chalkos.kinds.Cluster.get {
      output = ''
        go-template={{$s := .spec}}{{range $s.ipFamilies}}pods {{index $s.podCIDRs .}}
        {{end}}{{range $s.vxlanSourceSubnets}}source {{.}}
        {{end}}'';
    };
  };

  # The reference plugins the image names (chalkos.cni.plugins), and with flannel its own plugin.
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
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.kubernetes = {
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
    };
  };

  config = lib.mkMerge [
    # Apart from the rest: the condition reads chalkos, so it can guard values below chalkos but
    # not a definition of chalkos as a whole.
    {
      chalkos.consumers = lib.mkIf enable {
        chalkos-registry-mirrors.kinds = [ "RegistryMirror" ];
        chalkos-controlplane.kinds = [ "Node" ];
        # Both start too: a node runs no kubelet until it holds a share, which the delivery of
        # its first one changes.
        chalkos-kubernetes.restart = {
          when = kubernetesChanged;
          start = true;
        };
        kubelet.restart = {
          when = kubernetesChanged;
          start = true;
        };
        # chalkd's Kubernetes loops, which chalkd starts again itself: every loop reads the
        # cluster when it starts, and the control plane's loop reads the share and applies the
        # control plane's objects.
        chalkd = {
          restart.when = clusterChanged;
          reload.when = "${shareChanged} || ${addonsApplied}";
        };
      };
      # The cluster file chalkd writes, which the kubelet's rules compare.
      chalkos.facts = lib.mkIf enable {
        cluster.file = "${run}/cluster.json";
      };
    }
    (lib.mkIf enable {
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
      # What the image fixes about Kubernetes, which its image directory's metadata.json names.
      system.build.chalkosKubernetesImage = imageFile;
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
        "chalkos/kubernetes/image.json".text = builtins.toJSON imageFile;
      };

      systemd.services.chalkos-registry-mirrors = {
        description = "Write containerd's registry mirrors from the config";
        wantedBy = [ "containerd.service" ];
        before = [ "containerd.service" ];
        after = [ "chalkos-config.service" ];
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
          "chalkos-config.service"
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
          "chalkos-config.service"
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
