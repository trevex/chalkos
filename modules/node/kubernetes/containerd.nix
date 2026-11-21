# containerd on images that run Kubernetes: the binaries, the CNI plugins the image names and the
# CRI settings.
{
  chalkosLib,
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
  # Where CNI DaemonSets such as Cilium's or Calico's copy their plugins by hostPath. containerd
  # searches it before the store's plugins, so a DaemonSet's plugin of the same name wins.
  daemonSetPlugins = "/opt/cni/bin";
  # The pod network's configuration. /etc is read-only, so it lives on VAR; /etc/cni/net.d, where
  # DaemonSets write it by default, links to it.
  confDir = "/var/lib/cni/net.d";

  # NixOS puts iptables on containerd's path, for the CNI plugins. A copy without iptables-apply
  # and ip6tables-apply, bash scripts; its tools are links to xtables-nft-multi within the copy,
  # so it references nothing of the original.
  iptables = pkgs.runCommand "iptables-${pkgs.iptables.version}" { } ''
    cp -a ${pkgs.iptables} $out
    chmod u+w $out/bin
    rm $out/bin/iptables-apply $out/bin/ip6tables-apply
  '';

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
  options.chalkos.kubernetes.containerd.allowMacAdmin = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Whether privileged containers may hold `CAP_MAC_ADMIN`, as the Security Profiles Operator
      needs. With it, such a container can switch the module policy to permissive, which
      `chalkctl status` then reports as tampered. No container ever holds `CAP_SYS_MODULE`: the
      kernel loads the modules pods need, on demand or at boot.
    '';
  };

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

    # containerd runs the CNI plugins, and portmap programs nftables with nft. NixOS's path, with
    # the copy of iptables in place of its own.
    systemd.services.containerd.path = lib.mkForce [
      pkgs.containerd
      pkgs.runc
      iptables
      pkgs.nftables
    ];
    # containerd's bounding set, which its shims, runc and every container inherit, leaves out
    # loading modules and administering the LSMs, IPE's module policy among them. One assignment:
    # a second line of CapabilityBoundingSet= would add its capabilities back.
    systemd.services.containerd.serviceConfig.CapabilityBoundingSet =
      "~CAP_SYS_MODULE"
      + lib.optionalString (!config.chalkos.kubernetes.containerd.allowMacAdmin) " CAP_MAC_ADMIN";

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
          cni = {
            bin_dirs = [
              daemonSetPlugins
              "${cniPlugins}/bin"
            ];
            conf_dir = confDir;
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
      serviceConfig = chalkosLib.internal.noExec // {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe pkgs.chalkos.chalkd} kubernetes containerd-config ${clusterSettings}";
      };
    };

    # Containers run from the snapshots below containerd's root, which VAR's noexec would stop, so
    # the root is an exec path, mounted before containerd starts. Set-user-ID files take effect:
    # images carry them, such as ping and sudo, and a pod's securityContext is where to restrict
    # them. Device files stay inert.
    chalkos.storage.execPaths."/var/lib/containerd".suid = true;
    systemd.services.containerd.unitConfig.RequiresMountsFor = [ "/var/lib/containerd" ];

    # The plugins DaemonSets install, on a tmpfs of its own that allows running them. It is empty
    # at each boot until the DaemonSets copy their plugins again.
    fileSystems.${daemonSetPlugins} = {
      device = "tmpfs";
      fsType = "tmpfs";
      options = [
        "mode=0755"
        "nosuid"
        "nodev"
      ];
    };

    environment.etc."cni/net.d".source = confDir;

    systemd.tmpfiles.rules = [
      "d ${confDir} 0755 root root -"
    ];
  };
}
