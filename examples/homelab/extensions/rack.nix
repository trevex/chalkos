# Records each node's rack position and makes it available to a service on that node.
{ config, lib, ... }:
{
  options.chalkos.rack.enable = lib.mkEnableOption "rack position reporting";

  options.chalkos.nodes = lib.mkOption {
    type = lib.types.attrsOf (
      lib.types.submodule {
        options.rack.location = lib.mkOption {
          type = lib.types.str;
          example = "rack-a/u12";
          description = "Rack and height unit the node is mounted in.";
        };
      }
    );
  };

  config = lib.mkIf config.chalkos.rack.enable {
    chalkos.roles = lib.genAttrs [ "controlplane" "worker" ] (_: {
      nixosModules = [
        (
          { pkgs, ... }:
          {
            systemd.services.rack-location = {
              wantedBy = [ "multi-user.target" ];
              serviceConfig = {
                Type = "oneshot";
                ExecStart = "${pkgs.coreutils}/bin/cat %d/rack.location";
              };
            };
            chalkos.node.consumers.rack-location.keys = [ "rack.location" ];
          }
        )
      ];
    });
  };
}
