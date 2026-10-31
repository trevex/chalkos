# The router's kernel settings, which the output router-sysctl renders from the node's Router
# document into /run/sysctl.d. systemd-sysctl applies them at boot, once chalkos-config rendered
# them, and again when they change.
{
  chalkosPackages,
  config,
  lib,
  ...
}:
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.outputs.router-sysctl = {
      inputs = [ "Router" ];
      render = "${lib.getExe chalkosPackages.chalkos-render} router-sysctl";
      dir = "/run/sysctl.d";
      examples = config.chalkos.router.examples;
    };
    chalkos.effects.router-sysctl = {
      outputs = [ "router-sysctl" ];
      restart = [ "systemd-sysctl.service" ];
    };
    systemd.services.systemd-sysctl.after = [ "chalkos-config.service" ];
    # net.netfilter.nf_conntrack_max exists once nf_conntrack is loaded, which
    # systemd-modules-load does before systemd-sysctl runs.
    boot.kernelModules = [ "nf_conntrack" ];
  };
}
