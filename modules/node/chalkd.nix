# chalkd, the node agent, and the unit that applies the node's identity at boot. Images built for
# a cluster with an OS CA carry it, so chalkd in maintenance mode only accepts the cluster's
# clients.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  chalkd = pkgs.callPackage ../../nix/chalkd.nix { };
  inherit (config.chalkos.cluster) osCA;
in
{
  systemd.services.chalkos-identity = {
    description = "Apply the node identity";
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
      ExecStart = "${lib.getExe chalkd} load-identity";
    };
  };

  systemd.services.chalkd = {
    description = "chalkos node agent";
    wantedBy = [ "multi-user.target" ];
    after = [
      "network.target"
      "chalkos-identity.service"
      # Install enrolls TPM2 keyslots. Pulling tpm2.target in would wait for a TPM on nodes
      # that have none.
      "tpm2.target"
    ];
    # chalkd is the only way to reach the node, so it never stops restarting.
    startLimitIntervalSec = 0;
    # Tools Install, ApplyIdentity and ResetVolume run; repart formats with the mkfs tools.
    path = [
      config.systemd.package
      pkgs.cryptsetup
      pkgs.util-linux
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
      # cryptsetup and systemd-cryptenroll use the kernel crypto API through AF_ALG.
      RestrictAddressFamilies = [
        "AF_UNIX"
        "AF_INET"
        "AF_INET6"
        "AF_NETLINK"
        "AF_ALG"
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
      jq -er .osCA.certificate ${osCA} > $out
    '';
  };
}
