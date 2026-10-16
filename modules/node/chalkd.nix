# chalkd, the node agent, and the unit that applies the node's identity and config at boot. Images
# built with an OS CA carry it, so chalkd in maintenance mode only accepts that CA's clients.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  chalkd = pkgs.callPackage ../../nix/chalkd.nix { };
  inherit (config.chalkos.maintenance) osCA;
in
{
  # The image's own name for the input differs from mkImages's osCA, so the options reference
  # names each once.
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.maintenance.osCA = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = ''
          Public part of the cluster's secrets (`secrets.pub.json` from `chalkctl gen secrets`),
          whose OS CA the image carries, so chalkd in maintenance mode accepts only clients with a
          certificate from it. null builds an image that accepts any client until it is installed.
          mkImages sets it, in every image and the installer, from its `osCA`, which checks the
          file's version; the image's build checks it again.
        '';
      };
    };
  };

  config = {
    systemd.services.chalkos-config = {
      description = "Apply the node's config";
      wantedBy = [ "sysinit.target" ];
      # STATE is mounted by the initrd; networkd and every service that reads a credential come
      # later.
      after = [ "local-fs.target" ];
      before = [
        "sysinit.target"
        "network-pre.target"
        "systemd-networkd.service"
      ];
      wants = [ "network-pre.target" ];
      unitConfig.DefaultDependencies = false;
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe chalkd} load-config";
      };
    };

    systemd.services.chalkd = {
      description = "chalkos node agent";
      wantedBy = [ "multi-user.target" ];
      after = [
        "network.target"
        "chalkos-config.service"
        # Install enrolls TPM2 keyslots. Pulling tpm2.target in would wait for a TPM on nodes
        # that have none.
        "tpm2.target"
        # Not chalkos-kubernetes.service: chalkd must answer while the preparation waits for the
        # node's address. Bootstrap and the control plane's loop wait for its marker instead.
      ];
      # chalkd is the only way to reach the node, so it never stops restarting.
      startLimitIntervalSec = 0;
      # Tools Install, ApplyConfig and ResetVolume run; repart formats with the mkfs tools, the
      # ESP of a disk the installer lays out with dosfstools.
      path = [
        config.systemd.package
        pkgs.cryptsetup
        pkgs.util-linux
        pkgs.dosfstools
        pkgs.e2fsprogs
        pkgs.xfsprogs
        pkgs.btrfs-progs
        pkgs.efibootmgr
      ];
      serviceConfig = {
        ExecStart = "${lib.getExe chalkd} serve";
        Restart = "always";
        RestartSec = 5;
        # The fingerprint and addresses must reach the console in maintenance mode.
        StandardOutput = "journal+console";
        StandardError = "journal+console";

        # chalkd mounts volumes the host must see, writes efivars and sets the hostname, so it gets
        # no file system or UTS namespace: no ProtectSystem, PrivateTmp, PrivateDevices,
        # ProtectHostname and the like. ProtectKernelLogs stays off too, as systemd gives it a
        # mount namespace that stops chalkd's mounts reaching the host.
        NoNewPrivileges = true;
        # cryptsetup and systemd-cryptenroll use the kernel crypto API through AF_ALG. A
        # control-plane node holding the cluster's VIPs announces them with gratuitous ARP through
        # AF_PACKET; it adds them through AF_NETLINK and sends neighbour advertisements on a raw
        # AF_INET6 socket. chalkd runs as root with its capabilities, which these need:
        # CAP_NET_ADMIN and CAP_NET_RAW.
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_INET"
          "AF_INET6"
          "AF_NETLINK"
          "AF_ALG"
          "AF_PACKET"
        ];
        LockPersonality = true;
        RestrictRealtime = true;
        SystemCallArchitectures = "native";
        ProtectClock = true;
      };
    };

    networking.firewall.allowedTCPPorts = [ 50000 ];

    environment.etc."chalkos/os-ca.crt" = lib.mkIf (osCA != null) {
      source = pkgs.runCommand "os-ca.crt" { nativeBuildInputs = [ pkgs.jq ]; } ''
        if ! jq -e '.version == 3' ${osCA} > /dev/null; then
          echo "${osCA} is not version 3; chalkos reads version 3 only, so generate new secrets with chalkctl gen secrets" >&2
          exit 1
        fi
        jq -er .osCA.certificate ${osCA} > $out
      '';
    };
  };
}
