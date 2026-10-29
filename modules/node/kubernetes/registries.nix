# The node's registries on images that run Kubernetes: containerd's mirrors and the kubelet's
# registry credentials, both from the Mirror and Registry documents.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosPackages) chalkd;
  # chalkd renders each registry's hosts.toml from the Mirror and Registry documents, so
  # containerd also reaches a registry over plain HTTP when its Registry allows it.
  registryMirrors = pkgs.callPackage ../../../nix/registry-mirrors.nix {
    mirrorsCommand = "${lib.getExe chalkd} kubernetes containerd-hosts";
  };
in
{
  config = lib.mkIf config.chalkos.kubernetes.enable {
    systemd.services.chalkos-registry-mirrors = {
      description = "Write containerd's registry hosts from the config";
      chalkos.kinds = [
        "Mirror"
        "Registry"
      ];
      wantedBy = [ "containerd.service" ];
      before = [ "containerd.service" ];
      after = [ "chalkos-config.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.getExe registryMirrors;
      };
    };

    # The kubelet's credential provider, which is chalkd, and the node's registry credentials,
    # from the Registry documents and the passwords chalkd gives the unit. chalkd keeps the
    # credentials in a directory readable by root alone.
    systemd.services.chalkos-registry-credentials = {
      description = "Write the kubelet's registry credentials from the config";
      chalkos.kinds = [ "Registry" ];
      wantedBy = [ "multi-user.target" ];
      before = [ "kubelet.service" ];
      after = [ "chalkos-config.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe chalkd} kubernetes registry-credentials";
      };
    };
  };
}
