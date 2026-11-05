# The validator router: chalkos-render validate router, which reads the documents of the router's
# kinds and of networkd's Network as the router's model takes them, pkg/router/derive's. chalkd
# runs it before the router's outputs render; chalkctl links the same code and runs it in-process
# for an image of the router kinds, on the same documents.
{
  chalkosPackages,
  config,
  lib,
  ...
}:
let
  # The kinds the model reads, as derive.Kinds lists them. A variant that leaves a router feature
  # out declares none of its kinds, and the validator reads the kinds the image declares, as
  # chalkctl's copy does.
  kinds = [
    "Router"
    "FirewallZone"
    "FirewallPolicy"
    "AddressSet"
    "NATRule"
    "BGP"
    "BGPPeer"
    "PrefixList"
    "CommunityList"
    "RoutingPolicy"
    "NAT64"
    "Resolver"
    "VRF"
    "PolicyRoute"
    "NextHop"
    "DHCPSubnet"
    "DHCPRelay"
    "Network"
  ];
  declared = lib.attrNames config.chalkos.kinds ++ (import ../../../lib/kinds/api.nix).networkdKinds;
in
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.validators.router = {
      inputs = lib.filter (kind: lib.elem kind declared) kinds;
      program = lib.getExe chalkosPackages.chalkos-render;
      examples = config.chalkos.router.examples;
    };
  };
}
