# The directories below a noexec file system that programs run from. VAR and the root tmpfs, which
# holds /var until the node is installed, are mounted noexec, so a layer whose services run
# programs from below /var declares the directories here. chalkos-storage's generator
# (storage.nix) mounts each onto itself with exec, or mounts the volume at it with these options.
{ lib, ... }:
let
  inherit (lib) mkOption types;
in
{
  options.chalkos.storage.execPaths = mkOption {
    type = types.attrsOf (
      types.submodule {
        options = {
          suid = mkOption {
            type = types.bool;
            default = false;
            description = "Whether set-user-ID and set-group-ID files below the path take effect.";
          };
          dev = mkOption {
            type = types.bool;
            default = false;
            description = "Whether device files below the path open as devices.";
          };
        };
      }
    );
    default = { };
    example = {
      "/var/lib/containerd".suid = true;
    };
    description = ''
      Directories programs run from, below the noexec VAR or root, by absolute path. Each is
      mounted onto itself with `exec` and `nosuid` or `suid`, `nodev` or `dev`, after the file
      systems it lies on, VAR or a Volume, and before the units that need it, which name it in
      `RequiresMountsFor`. A Volume mounted at the path itself takes these options instead of its
      own. Paths hold no spaces or `=`.
    '';
  };
}
