# Records each node's rack position as a Rack document and makes it available to a service on
# that node.
{ config, lib, ... }:
let
  inherit (config.chalkos.rack) enable;
in
{
  options.chalkos.rack.enable = lib.mkEnableOption "rack position reporting";

  options.chalkos.nodes = lib.mkOption {
    type = lib.types.attrsOf (
      lib.types.submodule (
        { config, ... }:
        {
          options.rack.location = lib.mkOption {
            type = lib.types.str;
            example = "rack-a/u12";
            description = "Rack and height unit the node is mounted in.";
          };
          # The node's position travels in its config as a document of the Rack kind.
          config.documents = lib.mkIf enable [
            {
              apiVersion = "rack.example.org/v1alpha1";
              kind = "Rack";
              metadata.name = "rack";
              spec.location = config.rack.location;
            }
          ];
        }
      )
    );
  };

  config = lib.mkIf enable {
    chalkos.roles.node = {
      nixosModules = [
        (
          { config, pkgs, ... }:
          {
            chalkos.kinds.Rack = {
              group = "rack.example.org";
              version = "v1alpha1";
              spec.location = lib.mkOption {
                type = lib.types.nonEmptyStr;
                example = "rack-a/u12";
                description = "Rack and height unit the node is mounted in.";
              };
            };
            # chalkd writes the node's Rack below /run/chalkos/config and restarts the unit when
            # it changes.
            chalkos.consumers.rack-location.kinds = [ "Rack" ];
            systemd.services.rack-location = {
              wantedBy = [ "multi-user.target" ];
              serviceConfig = {
                Type = "oneshot";
                RemainAfterExit = true;
                # A path the kind lacks fails the image's evaluation.
                ExecStart = pkgs.writeShellScript "rack-location" (
                  config.chalkos.kinds.Rack.get {
                    name = "rack";
                    path = "spec.location";
                  }
                );
              };
            };
          }
        )
      ];
    };
  };
}
