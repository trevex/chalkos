# The validator router: chalkos-render validate router, which reads the documents of the router's
# kinds and of networkd's Network as the router's model takes them, pkg/router/derive's. chalkd
# runs it before the router's outputs render; chalkctl links the same code and runs it in-process
# for an image of the router kinds, on the same documents.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosLib.internal.api) groups;
  # The router kinds the image declares, and Network. A variant that leaves a router feature out
  # declares none of its kinds, and chalkctl's copy reads only the kinds the image declares too.
  kinds = lib.attrNames (lib.filterAttrs (_: k: k.group == groups.router) config.chalkos.kinds) ++ [
    "Network"
  ];
in
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.validators.router = {
      inputs = kinds;
      program = lib.getExe pkgs.chalkos.chalkos-render;
      examples = config.chalkos.router.examples;
    };
  };
}
