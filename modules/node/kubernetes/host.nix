# What every Kubernetes image gives the workloads that integrate with the host: /lib/modules, as
# DaemonSets mount it by hostPath, and links below /usr for the programs operators run in the
# host's mount namespace through nsenter. /lib/modules leads to the image's module tree
# (kernel.nix), whose files are its own, so a pod that mounts it reads the modules without the
# store. /usr is the store's file system, read-only and checked by dm-verity, so the links are
# part of the image: chalkos.usr.links adds them to the store partition, and the layer's features
# and an image's chalkos.kubernetes.hostTools fill it.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (lib) mkOption types;
  links = config.chalkos.usr.links;
  # The links as a tree, one directory per first path component below /usr.
  tree = pkgs.runCommand "chalkos-usr-links" { } (
    ''
      mkdir $out
    ''
    + lib.concatStrings (
      lib.mapAttrsToList (path: target: ''
        mkdir -p "$out/$(dirname ${lib.escapeShellArg path})"
        ln -s ${lib.escapeShellArg target} $out/${lib.escapeShellArg path}
      '') links
    )
  );
  tops = lib.unique (map (p: lib.head (lib.splitString "/" p)) (lib.attrNames links));
  inherit (config.image.repart.verityStore.partitionIds) store;
in
{
  options.chalkos.usr.links = mkOption {
    type = types.attrsOf types.path;
    default = { };
    internal = true;
    description = ''
      Symbolic links below /usr by their path relative to it, such as `sbin/iscsiadm`, to
      programs or directories of the store. They live in the store's file system, so they change
      the image's root hash like any other content.
    '';
  };

  options.chalkos.kubernetes.hostTools = mkOption {
    type = types.attrsOf types.path;
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

    image.repart.partitions.${store} = lib.mkIf (links != { }) {
      storePaths = [ tree ];
      contents = lib.genAttrs (map (t: "/${t}") tops) (t: {
        source = "${tree}${t}";
      });
    };
  };
}
