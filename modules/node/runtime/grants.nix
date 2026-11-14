# Grants: a unit reads the config kinds of systemd.services.<unit>.chalkos.kinds. chalkd writes
# the node's documents of those kinds below /run/chalkos/config and the values their secretRef
# fields name into the unit's credentials, and chalkcfg is on its PATH. A grant never restarts
# the unit; an effect does (effects.nix).
{
  chalkosLib,
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosPackages) chalkcfg;
  checkKinds = chalkosLib.internal.checkKinds config.chalkos.kinds;

  grant =
    { name, config, ... }:
    {
      options.chalkos.kinds = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        example = [ "Peer" ];
        description = ''
          Config kinds the unit reads, by name: attributes of `chalkos.kinds`, or networkd's
          `Network`, `NetDev` and `Link`. The documents of a kind of `chalkos.kinds` are in its
          `dir`, networkd's below `/run/chalkos/config/networkd.chalkos.dev`, and `chalkcfg` is
          on the unit's `PATH`. The values their `secretRef` fields name are the unit's
          credentials `secret_<secret>.<key>`. A grant never restarts the unit: an effect of
          `chalkos.effects` does.
        '';
      };
      # systemd loads every file of the directory as the credential secret_<file>. chalkd writes
      # the directory, empty or not, for every granted unit when it loads the node's config. A node
      # without config, as in maintenance mode, has none, and neither has one whose chalkos-config
      # failed; the credential secret of SetCredential= then stands in for the directory, which
      # systemd would otherwise fail the unit for. A unit that needs its credentials checks for the
      # directory itself.
      config = lib.mkIf (config.chalkos.kinds != [ ]) {
        serviceConfig = {
          LoadCredential = checkKinds "systemd.services.${name}.chalkos.kinds" config.chalkos.kinds [
            "secret:/run/chalkos/credentials/${name}"
          ];
          # systemd ignores a SetCredential= of an empty value.
          SetCredential = [ "secret:none" ];
        };
        path = [ chalkcfg ];
      };
    };

  granted = lib.filterAttrs (_: s: s.chalkos.kinds != [ ]) config.systemd.services;

  # A grant on a name the image runs no service of creates an empty unit, as for a typo. The image's
  # build checks the units it installs, its packages' among them: each granted unit has ExecStart=
  # in its unit file or a drop-in. grep reads the files itself: grep -q in a pipe ends at its first
  # match, which can kill the writer with SIGPIPE and fail the pipe under pipefail. -s keeps quiet
  # about a unit without drop-ins, and a match still succeeds.
  grantsCheck = pkgs.runCommand "chalkos-grants-check" { } ''
    units=${config.environment.etc."systemd/system".source}
    for name in ${lib.escapeShellArgs (lib.attrNames granted)}; do
      if ! grep -qs '^ExecStart=' "$units/$name.service" "$units/$name.service.d"/*.conf; then
        echo "systemd.services.$name.chalkos.kinds grants kinds to $name.service, which runs nothing: neither its unit file nor a drop-in has ExecStart=" >&2
        exit 1
      fi
    done
    touch $out
  '';

  # What chalkd reads of each granted unit.
  servicesJSON = builtins.toJSON (
    lib.mapAttrs (name: s: {
      kinds = checkKinds "systemd.services.${name}.chalkos.kinds" s.chalkos.kinds s.chalkos.kinds;
    }) granted
  );
in
{
  options.systemd.services = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule grant);
  };

  config = {
    system.checks = [ grantsCheck ];
    system.build.chalkosServices = pkgs.writeText "chalkos-services.json" servicesJSON;
    environment.etc."chalkos/services.json".text = servicesJSON;
  };
}
