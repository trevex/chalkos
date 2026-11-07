# The Kubernetes layer, on images that run Kubernetes: containerd and the kubelet of the image's
# Kubernetes release, the layer's marker /etc/chalkos/layers/kubernetes, and the image's Kubernetes
# facts in /etc/chalkos/kubernetes/image.json. chalkd builds the node's cluster from them and the
# node's config, and writes everything node-specific below /run/chalkos/kubernetes before the
# kubelet starts.
{
  chalkosPackages,
  config,
  options,
  lib,
  pkgs,
  ...
}:
let
  k = config.chalkos.kubernetes;
  releases = import ./releases.nix { inherit lib pkgs; };
  # Whether the image's modules define an option, so that its default does not stand.
  defined = o: o.highestPrio < (lib.mkOptionDefault null).priority;
  inherit (k) enable;
  inherit (chalkosPackages) chalkd;
  # The kubelet alone: the package holds every component, which control planes run from images.
  kubelet = pkgs.runCommand "kubelet-${k.package.version}" { } ''
    install -Dm755 ${k.package}/bin/kubelet $out/bin/kubelet
  '';
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
    ./containerd.nix
    ./registries.nix
    ./firewall.nix
    ./kinds.nix
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
    version = lib.mkOption {
      type = lib.types.enum (lib.attrNames releases);
      default = lib.versions.majorMinor pkgs.kubernetes.version;
      defaultText = lib.literalExpression "lib.versions.majorMinor pkgs.kubernetes.version";
      example = "1.36";
      description = ''
        Minor release of Kubernetes whose kubelet the image runs, which selects
        `chalkos.kubernetes.package`: nixpkgs's own minor, whose package the binary cache holds,
        or one of the two minors before it. chalkos pins each of those two to a patch release and
        the image's build compiles its kubelet from source.
      '';
    };
    package = lib.mkOption {
      type = lib.types.package;
      default = releases.${k.version};
      defaultText = lib.literalMD "the release `chalkos.kubernetes.version` selects";
      description = ''
        Kubernetes release whose kubelet the image runs; image.json names its version. The
        Cluster document names the images of the control plane, kube-proxy, CoreDNS, flannel and
        the pod sandbox, and the image's checker holds their versions to Kubernetes's version
        skew policy against this kubelet's. Set it for a release `chalkos.kubernetes.version`
        does not offer, such as another patch release; an image that sets both names one minor
        in both.
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
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.mkDefault "${lib.getExe chalkd} kubernetes prepare";
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
