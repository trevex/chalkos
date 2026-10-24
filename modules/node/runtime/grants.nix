# Grants: a unit reads the config kinds of systemd.services.<unit>.chalkos.kinds. chalkd writes
# the node's documents of those kinds below /run/chalkos/config and the values their secretRef
# fields name into the unit's credentials, and chalkcfg is on its PATH. A grant never restarts
# the unit; an effect does (effects.nix).
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosPackages) chalkcfg;
  checkKinds = import ../../../lib/kinds/check.nix {
    inherit lib;
    inherit (config.chalkos) kinds;
  };

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
      # systemd loads every file of the directory as the credential secret_<file>; chalkd keeps
      # the directory, empty or not, for every granted unit.
      config = lib.mkIf (config.chalkos.kinds != [ ]) {
        serviceConfig.LoadCredential =
          checkKinds "systemd.services.${name}.chalkos.kinds" config.chalkos.kinds
            [ "secret:/run/chalkos/credentials/${name}" ];
        path = [ chalkcfg ];
      };
    };

  granted = lib.filterAttrs (_: s: s.chalkos.kinds != [ ]) config.systemd.services;

  # A grant on a name the image defines no service of creates an empty unit, as for a typo. A
  # warning, not an assertion: units from systemd.packages have no ExecStart in the config.
  orphan =
    name: s:
    lib.optional (!(s.serviceConfig ? ExecStart || s.script != "")) ''
      systemd.services.${name}.chalkos.kinds may grant kinds to no service: systemd.services.${name}
      defines neither serviceConfig.ExecStart nor script. Ignore this if the unit comes from
      systemd.packages.
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
    warnings = lib.concatLists (lib.mapAttrsToList orphan granted);
    system.build.chalkosServices = pkgs.writeText "chalkos-services.json" servicesJSON;
    environment.etc."chalkos/services.json".text = servicesJSON;
  };
}
