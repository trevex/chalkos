{ lib, ... }:
{
  options.chalkos.warnings = lib.mkOption {
    type = lib.types.listOf lib.types.str;
    default = [ ];
    internal = true;
    description = "Messages shown when the manifest is evaluated.";
  };
}
