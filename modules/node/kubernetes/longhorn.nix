# Longhorn: iscsid, started at boot, and iscsiadm, which Longhorn runs in the namespaces of the
# process named iscsid it finds; the NFS client of its RWX volumes, whose mounts Longhorn's CSI
# plugin runs through mount in the host's namespaces; and host-tool links for the programs it runs
# there. open-iscsi and nfs-utils come as copies of the programs alone, without the shell and
# Python scripts of the originals, which the copies do not reference.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  k = config.chalkos.kubernetes;
  # iscsiadm's database below its root, /etc/iscsi, which the read-only /etc links to VAR.
  db = "/var/lib/iscsi";
  dbDirs = [
    "ifaces"
    "isns"
    "nodes"
    "send_targets"
    "static"
  ];
  # The node's initiator name, made on its first boot from that boot's machine ID: every node
  # boots the same image.
  initiatorName = "${db}/initiatorname.iscsi";
  # Copies of ELF programs and libraries whose run paths name the copy's libraries in place of
  # the original's, and each package of relink in place of the one it replaces, which the copy
  # must not reference; fix runs on the copy, before the reference check.
  copyPrograms =
    {
      pname,
      from,
      programs,
      libs ? [ ],
      relink ? [ ],
      fix ? "",
    }:
    pkgs.runCommand "${pname}-${from.version}"
      {
        inherit pname;
        inherit (from) version;
        nativeBuildInputs = [
          pkgs.patchelf
          pkgs.removeReferencesTo
        ];
        disallowedReferences = [ from ] ++ map (r: r.from) relink;
      }
      ''
        mkdir -p $out/bin $out/lib
        ${lib.concatMapStrings (p: "cp ${from}/bin/${p} $out/bin/\n") programs}
        ${lib.concatMapStrings (l: "cp -P ${from}/lib/${l} $out/lib/\n") libs}
        chmod -R u+w $out
        for f in $out/bin/* $out/lib/*; do
          [ -L "$f" ] && continue
          rpath=$(patchelf --print-rpath "$f")
          old=${from}/lib
          rpath=''${rpath//"$old"/$out/lib}
          ${lib.concatMapStrings (r: ''
            old=${r.from}/lib
            rpath=''${rpath//"$old"/${r.to}/lib}
          '') relink}
          patchelf --set-rpath "$rpath" "$f"
        done
        ${fix}
      '';
  iscsi = copyPrograms {
    pname = "open-iscsi-chalkos";
    from = pkgs.openiscsi;
    programs = [
      "iscsiadm"
      "iscsid"
    ];
    libs = [ "libopeniscsiusr.so*" ];
    # iscsid links libsystemd for sd_notify and its socket alone: the library of systemd's
    # libraries, which the image holds, in place of the whole of nixpkgs's systemd, which it does
    # not.
    relink = [
      {
        from = pkgs.systemd;
        to = lib.getLib pkgs.systemdLibs;
      }
    ];
    fix = "install -Dm644 ${pkgs.openiscsi}/etc/iscsi/iscsid.conf $out/etc/iscsi/iscsid.conf";
  };
  # mount.nfs names start-statd, a shell script that starts rpc.statd for the locks of NFSv3,
  # which the copy leaves out; Longhorn mounts its RWX volumes with NFSv4.1, which needs no
  # statd.
  nfs = copyPrograms {
    pname = "nfs-utils-chalkos";
    from = pkgs.nfs-utils;
    programs = [ "mount.nfs" ];
    fix = ''
      remove-references-to -t ${pkgs.nfs-utils} $out/bin/mount.nfs
      for name in mount.nfs4 umount.nfs umount.nfs4; do
        ln -s mount.nfs $out/bin/$name
      done
    '';
  };
in
{
  options.chalkos.kubernetes = chalkosLib.internal.feature "Kubernetes" "longhorn" {
    carries = "what Longhorn runs on the host: iscsid and iscsiadm, the NFS client, the `iscsi_tcp` and `dm_crypt` kernel modules, loaded at boot, and links for `iscsiadm`, `fstrim`, `cryptsetup`, `dmsetup` and `mount` in `/usr/bin` and `/usr/sbin`";
    default = false;
  };

  config = lib.mkIf (k.enable && k.longhorn.enable) {
    # Longhorn's node check looks for dm_crypt as loaded; iSCSI's would load on the first login.
    boot.kernelModules = [
      "iscsi_tcp"
      "dm_crypt"
    ];

    # mount runs mount.nfs from the system path.
    environment.systemPackages = [ nfs ];

    chalkos.kubernetes.hostTools = {
      iscsiadm = "${iscsi}/bin/iscsiadm";
      fstrim = "${pkgs.util-linux}/bin/fstrim";
      cryptsetup = "${lib.getBin pkgs.cryptsetup}/bin/cryptsetup";
      dmsetup = "${lib.getBin pkgs.lvm2}/bin/dmsetup";
      # The CSI plugin mounts RWX volumes with mount in the host's mount namespace, so that the
      # mounts outlive the plugin.
      mount = "${pkgs.util-linux}/bin/mount";
    };

    environment.etc = {
      "iscsi/iscsid.conf".source = "${iscsi}/etc/iscsi/iscsid.conf";
      "iscsi/initiatorname.iscsi".source = initiatorName;
    }
    // lib.listToAttrs (map (d: lib.nameValuePair "iscsi/${d}" { source = "${db}/${d}"; }) dbDirs);

    systemd.tmpfiles.settings.chalkos-iscsi = {
      ${db}.d.mode = "0700";
      # Written once: %m is the machine ID of the boot that writes it.
      ${initiatorName}.f = {
        mode = "0600";
        argument = "InitiatorName=iqn.2026-11.dev.chalkos:%m";
      };
    }
    // lib.genAttrs (map (d: "${db}/${d}") dbDirs) (_: {
      d.mode = "0700";
    });

    # open-iscsi's units, with the copy's iscsid. Longhorn finds iscsid by its name among the
    # host's processes, so it runs from boot; the socket starts it again should it stop.
    systemd.sockets.iscsid = {
      description = "Open-iSCSI iscsid socket";
      wantedBy = [ "sockets.target" ];
      listenStreams = [ "@ISCSIADM_ABSTRACT_NAMESPACE" ];
    };
    systemd.services.iscsid = {
      description = "Open-iSCSI";
      wantedBy = [ "multi-user.target" ];
      after = [
        "network.target"
        "systemd-tmpfiles-setup.service"
      ];
      before = [ "remote-fs-pre.target" ];
      unitConfig.RequiresMountsFor = [ db ];
      # No mount namespace of its own: Longhorn runs iscsiadm in iscsid's, which must be the
      # host's, so the sandbox holds what needs none. iscsid locks its memory, raises its
      # priority, keeps clear of the OOM killer, loads transport modules and drives iSCSI's
      # netlink and its connections.
      serviceConfig = {
        Type = "notify";
        NotifyAccess = "main";
        ExecStart = "${iscsi}/bin/iscsid -f";
        KillMode = "mixed";
        Restart = "on-failure";
        CapabilityBoundingSet = [
          "CAP_SYS_ADMIN"
          "CAP_NET_ADMIN"
          "CAP_NET_RAW"
          "CAP_IPC_LOCK"
          "CAP_SYS_NICE"
          "CAP_SYS_RESOURCE"
          "CAP_SYS_MODULE"
          "CAP_DAC_OVERRIDE"
        ];
        NoNewPrivileges = true;
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_NETLINK"
          "AF_INET"
          "AF_INET6"
        ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        SystemCallArchitectures = "native";
        UMask = "0077";
      };
    };
  };
}
