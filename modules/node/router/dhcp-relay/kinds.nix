# DHCP relays on router images whose chalkos.router.dhcpRelay.enable is true: the links whose
# clients' DHCP requests the router forwards to DHCP servers elsewhere, one document each, which
# the output dhcp-relay renders for dnsmasq's relay of the VRF the links are in (default.nix). A
# relay names each of its links, as a DHCPSubnet does.
{ lib }:
let
  inherit (lib) mkOption types;
  inherit (lib.chalkos.internal) builtin;
  inherit (lib.chalkos.internal.api) groups;
  inherit (import ../types.nix { inherit lib; }) interfaceName address;
in
{
  DHCPRelay = builtin groups.router // {
    multiple = true;
    description = "Links whose clients' DHCP requests the router relays to DHCP servers elsewhere, DHCPv4 to the IPv4 servers and DHCPv6 to the IPv6 ones.";
    spec = {
      # A link takes one relay, and a relay names it once.
      interfaces = lib.chalkos.mkOption {
        type = types.nonEmptyListOf interfaceName;
        unique.hint = "a link takes one relay";
        example = [ "vlan20" ];
        description = "Links the clients ask on, each by its full name, without a glob: links of one VRF, each of which a Network document matching it by Name= alone gives a static address of each family the servers have, a global one for IPv6. A link takes one relay.";
      };
      servers = lib.chalkos.mkOption {
        type = types.nonEmptyListOf address;
        example = [
          "198.51.100.53"
          "2001:db8:ff::53"
        ];
        description = "Unicast addresses of the DHCP servers, which the router reaches through the links' VRF: IPv4 servers get the links' DHCPv4 requests and IPv6 servers their DHCPv6 requests. A DHCPv6 server answers relayed requests on such an address, as Kea's does on an interface given as <name>/<address>.";
        rules = [
          {
            description = "A relay's servers are unicast addresses.";
            rule = ''self.all(a, !["0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3", "::/8", "fe80::/10", "ff00::/8"].exists(r, cidr(r).contains(ip(a))))'';
            messageExpression = ''"the server " + self.filter(a, ["0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3", "::/8", "fe80::/10", "ff00::/8"].exists(r, cidr(r).contains(ip(a))))[0] + " is no unicast address a relay forwards to"'';
          }
        ];
        unique = {
          across = "list";
          as = "ip";
          hint = "name each server once";
        };
      };
    };
  };
}
