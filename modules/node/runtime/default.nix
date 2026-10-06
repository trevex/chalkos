# Runtime access to node-specific values. The identity loader writes /run/chalkos/node.json and
# one credential file per key; units declare the keys they read so chalkd can restart them when
# a key changes.
{ config, lib, ... }:
let
  cfg = config.chalkos.node;
  credentialPath = key: "/run/chalkos/credentials/${key}";
  consumer = lib.types.submodule {
    options = {
      keys = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        example = [ "longhorn.diskPath" ];
        description = "Identity keys (dotted paths) the unit reads, each as a systemd credential of the same name.";
      };
      restartOnChange = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Restart the unit when one of its keys changes.";
      };
    };
  };
in
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.node = {
        file = lib.mkOption {
          type = lib.types.str;
          readOnly = true;
          default = "/run/chalkos/node.json";
          description = "Path of the node's identity (without secrets) on the running node.";
        };
        consumers = lib.mkOption {
          type = lib.types.attrsOf consumer;
          default = { };
          description = "Services, by name, that read node-specific values.";
        };
      };
    };
  };

  config = {
    # The credential settings below would otherwise create an empty unit for a misspelled name.
    assertions = lib.mapAttrsToList (
      name: _:
      let
        service = config.systemd.services.${name};
      in
      {
        assertion = service.serviceConfig ? ExecStart || service.script != "";
        message = ''
          chalkos.node.consumers.${name} names no service: systemd.services.${name} defines
          neither serviceConfig.ExecStart nor script.
        '';
      }
    ) cfg.consumers;

    systemd.services = lib.mapAttrs (_: c: {
      serviceConfig.LoadCredential = map (key: "${key}:${credentialPath key}") c.keys;
    }) cfg.consumers;

    environment.etc."chalkos/consumers.json".text = builtins.toJSON (
      lib.mapAttrs (_: c: { inherit (c) keys restartOnChange; }) cfg.consumers
    );
  };
}
