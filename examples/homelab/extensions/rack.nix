# The Rack kind, which records a node's rack position, and a unit that reads it on the node. A
# variant that carries this module accepts Rack documents. A node that was surveyed, whose
# rack-survey tool wrote where it found the node, takes no other location than that once it runs;
# an install reads no facts and takes any.
{
  config,
  lib,
  pkgs,
  chalkosLib,
  ...
}:
{
  chalkos.kinds.Rack = {
    group = "rack.example.org";
    version = "v1alpha1";
    spec.location =
      lib.mkOption {
        type = lib.types.nonEmptyStr;
        example = "rack-a/u12";
        description = "Rack and height unit the node is mounted in.";
      }
      // {
        "x-chalkos-rules" = [
          {
            rule = "facts.rackSurvey == null || self == facts.rackSurvey.location";
            messageExpression = ''"the survey found the node at " + facts.rackSurvey.location + ", not at " + self + "; fix the location, or survey the node again"'';
          }
        ];
      };
  };
  # What the rack survey found, {"location": "rack-a/u12"}; null on a node not surveyed yet.
  chalkos.facts.rackSurvey.file = "/var/lib/rack-survey/location.json";
  # The node's Rack as a file, an output that changes when the Rack does, which restarts the
  # unit.
  chalkos.outputs.rack = {
    inputs = [ "Rack" ];
    render = chalkosLib.render.documents [ "Rack" ];
  };
  chalkos.effects.rack-location = chalkosLib.restartOn [ "rack" ] [ "rack-location.service" ];
  systemd.services.rack-location = {
    # chalkd writes the node's Rack below /run/chalkos/config for it.
    chalkos.kinds = [ "Rack" ];
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
