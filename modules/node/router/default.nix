# The router layer, on images whose nodes route between networks: its marker, forwarding, the
# router kinds' outputs and effects, the firewall that takes the place of NixOS's, BGP with FRR,
# NAT64 with Jool, the resolver with Unbound, VRFs, policy routing, DHCP with Kea, DHCP relays
# with dnsmasq, the router's diagnostics and the validator router.
{ config, lib, ... }:
let
  # The option chalkos.router.<feature>.enable of a feature of the layer, which images carry
  # unless a variant leaves it out.
  feature =
    description:
    lib.mkOption {
      type = lib.types.bool;
      default = true;
      inherit description;
    };
in
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
    ./dhcp-relay.nix
    ./validator.nix
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

  options.chalkos.router.bgp.enable = feature ''
    Whether router images carry FRR, which runs BGP and BFD, and accept the kinds BGP, BGPPeer,
    PrefixList, CommunityList and RoutingPolicy; only with the router layer. Set it false in a
    variant's modules to leave FRR and the Python of frr-reload.py out of its images.
  '';

  options.chalkos.router.nat64.enable = feature ''
    Whether router images carry Jool, the kernel module and tool of the NAT64 instances, and
    accept the kind NAT64; only with the router layer. Set it false in a variant's modules to
    leave Jool out of its images.
  '';

  options.chalkos.router.resolver.enable = feature ''
    Whether router images carry Unbound, the DNS resolvers of the router's VRFs, and accept the
    kind Resolver; only with the router layer. Set it false in a variant's modules to leave
    Unbound out of its images.
  '';

  options.chalkos.router.dhcp.enable = feature ''
    Whether router images carry the DHCP server, Kea, and accept the kind DHCPSubnet; only
    with the router layer. Set it false in a variant's modules to leave Kea out of its images,
    whose build then needs no local build of Kea.
  '';

  options.chalkos.router.dhcpRelay.enable = feature ''
    Whether router images carry the DHCP relay, dnsmasq, and accept the kind DHCPRelay; only
    with the router layer. Set it false in a variant's modules to leave dnsmasq out of its
    images.
  '';

  options.chalkos.router.examples = lib.mkOption {
    type = lib.types.path;
    internal = true;
    readOnly = true;
    default =
      let
        # The kinds the image accepts: its own and networkd's.
        accepted = lib.attrNames config.chalkos.kinds ++ (import ../../../lib/kinds/api.nix).networkdKinds;
        # The kinds of an example's documents. Each document names its kind on an unindented
        # kind: line of its own, bare or quoted.
        kindsOf =
          file:
          map (m: lib.elemAt m 1) (
            lib.filter lib.isList (
              builtins.split "(^|\n)kind:[ \t]*[\"']?([A-Za-z0-9]+)" (builtins.readFile file)
            )
          );
      in
      builtins.path {
        name = "router-examples";
        path = ../examples/router;
        # A link would be copied as it is, past the kinds' filter, so the examples are files.
        filter =
          file: type:
          if type == "directory" then
            true
          else if type == "regular" then
            lib.all (k: lib.elem k accepted) (kindsOf file)
          else
            throw "the router example ${baseNameOf file} is a ${type}, not a file";
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

    # zebra installs BGP's routes and next hops, and chalkos-nat64 the routes to the NAT64s'
    # links, which networkd would remove as foreign ones each time it configures a link.
    systemd.network.config.networkConfig = {
      ManageForeignRoutes = false;
      ManageForeignNextHops = false;
    };
  };
}
