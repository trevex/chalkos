# Links below /usr to programs or directories of the store. /usr is the store's file system,
# read-only and checked by dm-verity, so the links are part of the image: chalkos.usr.links adds
# them to the store partition, on every image that sets any. A layer fills it, as the Kubernetes
# layer does from chalkos.kubernetes.hostTools.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (config.chalkos.usr) links;
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
  options.chalkos.usr.links = lib.mkOption {
    type = lib.types.attrsOf lib.types.path;
    default = { };
    internal = true;
    description = ''
      Symbolic links below /usr by their path relative to it, such as `sbin/iscsiadm`, to
      programs or directories of the store. They live in the store's file system, so they change
      the image's root hash like any other content.
    '';
  };

  config = lib.mkIf (links != { }) {
    # The system references the tree, so the store partition, the SBOM and the shell checks hold its
    # closure.
    system.extraDependencies = [ tree ];
    image.repart.partitions.${store}.contents = lib.genAttrs (map (t: "/${t}") tops) (t: {
      source = "${tree}${t}";
    });
  };
}
