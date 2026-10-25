# The router layer, on images whose nodes route between networks: its marker, and forwarding.
{ config, lib, ... }:
{
  options.chalkos.router.enable = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Whether the image carries the router layer, which forwards IPv4 and IPv6 packets between
      the node's interfaces. An image carries the Kubernetes layer or the router layer, not both.
      mkImages sets it from the variant's `router`; the installer carries no layer.
    '';
  };

  config = lib.mkIf config.chalkos.router.enable {
    chalkos.layers.router = "";
    chalkos.forwarding.enable = true;
  };
}
