# The option types the router kinds share: interfaces, address ranges, addresses and families.
{ lib }:
let
  inherit (lib) types;
in
{
  # An interface's name, or a glob of names: a prefix and `*`, the one wildcard nftables matches.
  interface = types.strMatching "[A-Za-z0-9_.:@-]{1,15}|[A-Za-z0-9_.:@-]{0,14}[*]";
  interfaceText = "by name or glob, a glob being a prefix followed by `*`";
  # An interface's name alone.
  interfaceName = types.strMatching "[A-Za-z0-9_.:@-]{1,15}";
  # An address range, which the rules read with cidr(), and an address, which they read with ip().
  prefix = types.strMatching "[0-9A-Fa-f:.]+/[0-9]{1,3}";
  address = types.strMatching "[0-9A-Fa-f:.]+";
  family = types.enum [
    "ipv4"
    "ipv6"
  ];
}
