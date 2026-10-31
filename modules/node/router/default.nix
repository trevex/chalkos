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
      let
        # The kinds the image accepts: its own and networkd's.
        accepted = lib.attrNames config.chalkos.kinds ++ (import ../../../lib/kinds/api.nix).networkdKinds;
        # The kinds of an example's documents, as their top-level kind: lines name them.
        kindsOf =
          file:
          map (m: lib.elemAt m 1) (
            lib.filter lib.isList (builtins.split "(^|\n)kind:[ ]*([A-Za-z0-9]+)" (builtins.readFile file))
          );
      in
      builtins.path {
        name = "router-examples";
        path = ../examples/router;
        filter = file: type: type != "regular" || lib.all (k: lib.elem k accepted) (kindsOf file);
      };
    description = ''
      The examples the router outputs render at the image's build: the files of
      modules/node/examples/router all of whose documents are of kinds the image accepts. An
      image whose variant leaves a router feature out refuses that feature's kinds, and renders
      the examples without them.
    '';
  };

  config = lib.mkIf config.chalkos.router.enable {
    chalkos.layers.router = "";
    chalkos.forwarding.enable = true;
  };
}
