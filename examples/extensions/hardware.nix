# The Hardware kind, which records who made a node's server and which model it is, and a unit
# that logs it. The documents reach the unit as an output: an environment file rendered from a
# template, which a test renders from the example in hardware-examples. An image that
# carries this module accepts Hardware documents. The comments # --8<-- mark the sections the docs
# include.
# --8<-- [start:head]
{
  chalkosLib,
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
  # --8<-- [start:output]
  # hardware.env, with VENDOR, MODEL and ASSET_TAG; empty on a node without a Hardware document.
  chalkos.outputs.hardware = {
    inputs = [ "Hardware" ];
    render = chalkosLib.render.template ./hardware.env.tmpl;
  };
  # --8<-- [end:output]
  # --8<-- [end:head]
  # --8<-- [start:test]
  # The image accepts the example and renders the hardware.env of the golden file from it.
  chalkos.tests.hardware-server = {
    config = ./hardware-examples/server.yaml;
    outputs.hardware = ./tests/server/hardware;
  };
  # --8<-- [end:test]
  # --8<-- [start:tail]
  # The unit reads the file when it starts, so a changed file restarts it.
  chalkos.effects.hardware-info = chalkosLib.restartOn { outputs = [ "hardware" ]; } [
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
  # The unit is a bash script, so the image carries a shell.
  chalkos.shell.allow = true;
}
# --8<-- [end:tail]
