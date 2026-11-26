# The Kubernetes layer, on images that run Kubernetes: containerd and the kubelet of the image's
# Kubernetes release, the layer's marker /etc/chalkos/layers/kubernetes, and the image's Kubernetes
# facts in /etc/chalkos/kubernetes/image.json. chalkd builds the node's cluster from them and the
# node's config, and writes everything node-specific below /run/chalkos/kubernetes before the
# kubelet starts.
{
  chalkosLib,
  config,
  lib,
  options,
  pkgs,
  ...
}:
let
  k = config.chalkos.kubernetes;
  releases = import ./releases.nix;
  newest = lib.last (lib.sort lib.versionOlder (lib.attrNames releases));
  # Whether the image's modules define an option, so that its default does not stand.
  defined = o: o.highestPrio < (lib.mkOptionDefault null).priority;
  inherit (k) enable;
  inherit (pkgs.chalkos) chalkd;
  # The kubelet alone: a package of its own may hold every component, which control planes run
  # from images.
  kubelet = pkgs.chalkos.trimPackage {
    from = k.package;
    pname = "kubelet";
    keep = [ "kubelet" ];
  };
  # Where chalkd writes the node's Kubernetes files: RunDir in Go (pkg/kubernetes), whose
  # CredentialProviderDir is the credential-provider directory below it.
  run = "/run/chalkos/kubernetes";

  # Read by chalkd with the node's config: what the image fixes about Kubernetes. The Cluster
  # document names the images the cluster's components run from. A file made at evaluation, so
  # that its content is known without a build.
  imageFile = builtins.toFile "image.json" (
    builtins.toJSON {
      version = k.package.version;
      cni = config.chalkos.cni.provider;
    }
  );
in
{
  imports = [
    ./cni.nix
    ./effects.nix
    ./features.nix
    ./containerd.nix
    ./registries.nix
    ./firewall.nix
    ./host.nix
    ./kernel.nix
    ./kinds.nix
    ./ceph.nix
    ./kubevirt.nix
    ./longhorn.nix
    ./nvidia.nix
    ./bgp
    ./registry
  ];

  options.chalkos.kubernetes = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Whether the image runs Kubernetes: containerd, the kubelet and the preparation of the
        node's Kubernetes configuration. An image's modules set it by listing the Kubernetes layer,
        `chalkos.nixosModules.kubernetes`; the installer runs none.
      '';
    };
    version = lib.mkOption {
      type = lib.types.enum (lib.attrNames releases);
      default = newest;
      example = "1.36";
      description = ''
        Minor release of Kubernetes whose kubelet the image runs, which selects
        `chalkos.kubernetes.package`: one of the three newest minors, the newest by default.
        chalkos pins each to a patch release, whose kubelet the image's build compiles from
        source, so the image's nixpkgs does not choose the release.
      '';
    };
    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.callPackage ./kubelet.nix { inherit (releases.${k.version}) version hash; };
      defaultText = lib.literalMD "the release `chalkos.kubernetes.version` selects";
      description = ''
        Kubernetes release whose kubelet the image runs, whose version the image's Kubernetes
        facts name. The Cluster document names the images of the control plane, kube-proxy,
        CoreDNS, flannel and the pod sandbox, and the image's checker holds their versions to
        Kubernetes's version skew policy against this kubelet's. Set it for a release
        `chalkos.kubernetes.version` does not offer, such as another patch release; an image
        that sets both names one minor in both.
      '';
    };
  };

  config = lib.mkIf enable {
    assertions = [
      {
        assertion =
          !(defined options.chalkos.kubernetes.version && defined options.chalkos.kubernetes.package)
          || lib.versions.majorMinor k.package.version == k.version;
        message = "chalkos.kubernetes.package is Kubernetes ${k.package.version} and chalkos.kubernetes.version is ${k.version}; set one of them, or both to one minor";
      }
      # runc runs a container's init from a copy of itself in a memfd when it cannot clone its
      # binary otherwise, which vm.memfd_noexec = 2 forbids.
      {
        assertion = toString (config.boot.kernel.sysctl."vm.memfd_noexec" or 0) != "2";
        message = "vm.memfd_noexec is ${
          toString config.boot.kernel.sysctl."vm.memfd_noexec"
        }; Kubernetes images need it at most 1, as runc may run containers from a memfd";
      }
    ];

    chalkos.layers.kubernetes = "";
    chalkos.forwarding.enable = true;
    # The cluster file chalkd writes, which the kubelet's rules compare.
    chalkos.facts.cluster.file = "${run}/cluster.json";

    boot.kernelModules = [
      "overlay"
      "br_netfilter"
      "vxlan"
    ];
    boot.kernel.sysctl = {
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

    # What the image fixes about Kubernetes, on the node and as the layer's facts in its image
    # directory, which chalkctl checks config against.
    chalkos.imageDirectory.facts.kubernetes = imageFile;
    environment.etc."chalkos/kubernetes/image.json".source = imageFile;
    # The Kubernetes release on the image's OCI manifest, for registries and their users to read.
    chalkos.imageDirectory.annotations."dev.chalkos.kubernetes.version" = k.package.version;

    # Certificates, kubeconfigs and kubelet flags from the node's share and identity, prepared once
    # the node's addresses settled, waiting up to nodeIP.timeout for them. With flannel's VXLAN
    # rule, firewall.nix has chalkd fill its table too.
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
      serviceConfig = chalkosLib.sandbox.noExec // {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.mkDefault "${lib.getExe chalkd} kubernetes prepare";
      };
    };

    # Pods run programs from disk-backed emptyDir volumes, directories below the kubelet's root, as
    # in Tekton's or Argo's pattern of copying a binary into a shared emptyDir, so the root is an
    # exec path. Device files open as devices: raw block volumes are device nodes the kubelet
    # keeps below it. Flexvolume drivers, which the kubelet runs from below
    # /usr/libexec/kubernetes on the noexec root, are not supported.
    chalkos.storage.execPaths."/var/lib/kubelet".dev = true;

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
        # that is missing, and start empty there. The kubelet's root is an exec path.
        RequiresMountsFor = [
          "/var/lib/etcd"
          "/var/lib/kubelet"
        ];
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
