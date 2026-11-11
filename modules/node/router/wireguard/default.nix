# WireGuard on router images whose chalkos.router.wireguard.enable is true: the kinds WireGuard
# and WireGuardPeer, which the validator router checks.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.router.wireguard;
  routerLib = import ../lib.nix { inherit config lib pkgs; };
  kinds = import ./kinds.nix { inherit lib; };
in
{
  options.chalkos.router.wireguard.enable = routerLib.feature ''
    Whether router images accept the kinds WireGuard and WireGuardPeer; only with the router
    layer. Set it false in a variant's modules to leave WireGuard out of its images.
  '';

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;
  };
}
