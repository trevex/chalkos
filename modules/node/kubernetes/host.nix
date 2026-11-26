# What every Kubernetes image gives the workloads that integrate with the host: /lib/modules, as
# DaemonSets mount it by hostPath, and links below /usr for the programs operators run in the
# host's mount namespace through nsenter. /lib/modules leads to the image's module tree
# (kernel.nix), whose files are its own, so a pod that mounts it reads the modules without the
# store. The links are chalkos.usr.links of the base, which the layer's features and an image's
# chalkos.kubernetes.hostTools fill.
{
  config,
  lib,
  ...
}:
{
  options.chalkos.kubernetes.hostTools = lib.mkOption {
    type = lib.types.attrsOf lib.types.path;
    default = { };
    example = lib.literalExpression ''{ fstrim = "''${pkgs.util-linux}/bin/fstrim"; }'';
    description = ''
      Programs of the image, by name, that the image links into `/usr/bin` and `/usr/sbin`, where
      workloads find them when they run them in the host's mount namespace through `nsenter`, as
      Longhorn runs `fstrim`. Empty by default; features add theirs.
    '';
  };

  config = lib.mkIf config.chalkos.kubernetes.enable {
    assertions = map (n: {
      assertion = builtins.match "[A-Za-z0-9._+-]+" n != null;
      message = "chalkos.kubernetes.hostTools names ${n}, which is no file name";
    }) (lib.attrNames config.chalkos.kubernetes.hostTools);

    # The root is a tmpfs, so the link is made at each boot.
    systemd.tmpfiles.settings.chalkos-lib-modules = {
      "/lib".d.mode = "0755";
      "/lib/modules"."L+".argument = "${config.system.build.chalkosKernelModules}/lib/modules";
    };

    chalkos.usr.links = lib.concatMapAttrs (name: program: {
      "bin/${name}" = program;
      "sbin/${name}" = program;
    }) config.chalkos.kubernetes.hostTools;
  };
}
