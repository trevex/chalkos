# The router layer, on images whose nodes route between networks: its marker, forwarding, the
# router kinds' outputs and effects, the firewall that takes the place of NixOS's, BGP with FRR,
# NAT64 with Jool, the resolver with Unbound, VRFs, policy routing, DHCP with Kea and the
# router's diagnostics.
{ config, lib, ... }:
{
  imports = [
    ./firewall.nix
    ./sysctl.nix
    ./diagnostics.nix
    ./bgp.nix
    ./nat64.nix
    ./resolver.nix
    ./vrf.nix
    ./policy-routing.nix
    ./dhcp.nix
  ];

  options.chalkos.router.enable = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Whether the image carries the router layer, which forwards IPv4 and IPv6 packets between
      the node's interfaces and accepts the kinds of `router.chalkos.dev`. An image carries the
      Kubernetes layer or the router layer, not both. mkImages sets it from the variant's
      `router`; the installer carries no layer.
    '';
  };

  options.chalkos.router.dhcp.enable = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = ''
      Whether router images carry the DHCP server, Kea, and accept the kind DHCPSubnet; only
      with the router layer. Set it false in a variant's modules to leave Kea out of its images,
      whose build then needs no local build of Kea.
    '';
  };

  options.chalkos.router.examples = lib.mkOption {
    type = lib.types.path;
    internal = true;
    readOnly = true;
    default =
      if config.chalkos.router.dhcp.enable then
        ../examples/router
      else
        builtins.path {
          name = "router-examples";
          path = ../examples/router;
          filter = p: _: baseNameOf p != "dhcp.yaml";
        };
    description = ''
      The examples the router outputs render at the image's build: those of
      modules/node/examples/router, without dhcp.yaml on images without DHCP, whose schemas
      refuse its DHCPSubnet documents.
    '';
  };

  config = lib.mkIf config.chalkos.router.enable {
    chalkos.layers.router = "";
    chalkos.forwarding.enable = true;
  };
}
