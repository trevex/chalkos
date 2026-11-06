# containerd on images that run Kubernetes: the binaries, the CNI plugins the image names and the
# CRI settings.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  flannel = config.chalkos.cni.provider == "flannel";
  # containerd's settings from the node's config, which its own config imports.
  importDir = "/run/containerd/conf.d";
  clusterSettings = "${importDir}/chalkos.toml";

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
in
{
  config = lib.mkIf config.chalkos.kubernetes.enable {
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

    # containerd runs the CNI plugins, and portmap programs nftables with nft.
    systemd.services.containerd.path = [ pkgs.nftables ];

    environment.systemPackages = lib.mkIf config.chalkos.debug.tools [ pkgs.cri-tools ];

    virtualisation.containerd = {
      enable = true;
      settings = lib.mkForce {
        version = 4;
        # The settings of the Cluster document, which chalkos-containerd-config writes. The pattern
        # matches no file until the unit has written one, and containerd then starts with its own
        # settings.
        imports = [ "${importDir}/*.toml" ];
        plugins."io.containerd.cri.v1.images" = {
          # The hosts.toml files of the Mirror and Registry documents, which containerd reads on
          # every pull.
          registry.config_path = "/run/containerd/certs.d";
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

    # The pod sandbox's image, from the Cluster document. containerd also starts without the file,
    # with its own sandbox image, so containerd does not depend on this unit.
    systemd.services.chalkos-containerd-config = {
      description = "Write containerd's settings from the Cluster document";
      chalkos.kinds = [ "Cluster" ];
      wantedBy = [ "containerd.service" ];
      before = [ "containerd.service" ];
      after = [ "chalkos-config.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe chalkosPackages.chalkd} kubernetes containerd-config ${clusterSettings}";
      };
    };

    systemd.tmpfiles.rules = [
      "d /var/lib/cni/net.d 0755 root root -"
    ];
  };
}
