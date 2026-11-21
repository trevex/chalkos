# The node's registries on images that run Kubernetes: containerd's mirrors and the kubelet's
# registry credentials, both from the Mirror and Registry documents.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (pkgs.chalkos) chalkd;
in
{
  config = lib.mkIf config.chalkos.kubernetes.enable {
    # chalkd renders each registry's hosts.toml from the Mirror and Registry documents, so
    # containerd also reaches a registry over plain HTTP when its Registry allows it. It swaps the
    # files in for /run/containerd/certs.d in one rename, so containerd never reads a mix of two
    # configs nor finds none; a document chalkd cannot read leaves them as they were.
    systemd.services.chalkos-registry-mirrors = {
      description = "Write containerd's registry hosts from the config";
      # With the cluster registry, its endpoints and the node's static addresses, so the node
      # pulls from the endpoint it serves first.
      chalkos.kinds = [
        "Mirror"
        "Registry"
      ]
      ++ lib.optionals config.chalkos.kubernetes.registry.enable [
        "ClusterRegistry"
        "Network"
      ];
      wantedBy = [ "containerd.service" ];
      before = [ "containerd.service" ];
      after = [ "chalkos-config.service" ];
      serviceConfig = chalkosLib.internal.noExec // {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe chalkd} kubernetes registry-mirrors";
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
      serviceConfig = chalkosLib.internal.noExec // {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe chalkd} kubernetes registry-credentials";
      };
    };
  };
}
