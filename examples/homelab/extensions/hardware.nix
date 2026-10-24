# The Hardware kind, which records who made a node's server and which model it is, and a unit
# that logs it. The documents reach the unit as an output: an environment file rendered from a
# template, which the build renders from the examples in hardware-examples. A variant that
# carries this module accepts Hardware documents.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  chalkos.kinds.Hardware = {
    group = "hardware.example.org";
    version = "v1alpha1";
    description = "Who made the node's server and which model it is.";
    spec = {
      vendor = lib.mkOption {
        type = lib.types.nonEmptyStr;
        example = "Supermicro";
        description = "Vendor of the node's server.";
      };
      model = lib.mkOption {
        type = lib.types.nonEmptyStr;
        example = "SYS-E300-9D";
        description = "Model of the node's server.";
      };
      assetTag = lib.mkOption {
        type = lib.types.nullOr lib.types.nonEmptyStr;
        default = null;
        example = "HL-0042";
        description = "Asset tag of the server, if it has one.";
      };
    };
  };
  # hardware.env, with VENDOR, MODEL and ASSET_TAG; empty on a node without a Hardware document.
  chalkos.outputs.hardware = {
    inputs = [ "Hardware" ];
    render = lib.chalkos.render.template ./hardware.env.tmpl;
    examples = ./hardware-examples;
  };
  # The unit reads the file when it starts, so a changed file restarts it.
  chalkos.effects.hardware-info = lib.chalkos.restartOn { outputs = [ "hardware" ]; } [
    "hardware-info.service"
  ];
  systemd.services.hardware-info = {
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      # Active after it ran, so chalkd's try-restart runs it again.
      RemainAfterExit = true;
      EnvironmentFile = "-${config.chalkos.outputs.hardware.dir}/hardware.env";
      ExecStart = pkgs.writeShellScript "hardware-info" ''
        echo "hardware: ''${VENDOR:-unknown} ''${MODEL:-unknown}''${ASSET_TAG:+, asset tag $ASSET_TAG}"
      '';
    };
  };
}
