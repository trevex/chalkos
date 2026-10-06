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
    ];
    # Tools Install, ApplyIdentity and ResetVolume run; repart formats with the mkfs tools.
    path = [
      config.systemd.package
      pkgs.util-linux
      pkgs.e2fsprogs
      pkgs.xfsprogs
      pkgs.btrfs-progs
      pkgs.efibootmgr
    ];
    serviceConfig = {
      ExecStart = "${lib.getExe chalkd} serve";
      Restart = "always";
      RestartSec = 2;
      # The fingerprint and addresses must reach the console in maintenance mode.
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
  };

  networking.firewall.allowedTCPPorts = [ 50000 ];

  environment.etc."chalkos/os-ca.crt" = lib.mkIf (osCA != null) {
    source = pkgs.runCommand "os-ca.crt" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -er .osCA.certificate ${osCA} > $out
    '';
  };
}
