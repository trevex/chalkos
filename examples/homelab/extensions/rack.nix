# The Rack kind, which records a node's rack position, and a unit that reads it on the node. A
# variant that carries this module accepts Rack documents.
{
  config,
  lib,
  pkgs,
  ...
}:
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
  # chalkd writes the node's Rack below /run/chalkos/config and restarts the unit when it changes.
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
