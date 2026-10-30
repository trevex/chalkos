# The option types and CEL the router kinds share: interfaces, address ranges, addresses and
# families, and the names the Network documents match.
{ lib }:
let
  inherit (lib) types;
  inherit (import ./lib.nix) apiVersions;
  cel = import ../../../../lib/cel.nix { inherit lib; };
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
  # The names and globs of Name= in the [Match] sections of the Network documents of the config c,
  # each value, or each of a list, as one string between spaces: systemd splits a value at spaces
  # and tabs. A list of names would cost a loop more than a string.
  matchNames =
    c:
    let
      networks = cel.documents c apiVersions.networkd "Network";
    in
    ''cel.bind(nets, ${networks}, cel.bind(matches, dyn(nets.map(n, "match" in nets[n].spec ? ${cel.oneOrMany "nets[n].spec.match"} : []).flatten()), " " + dyn(matches.map(m, "name" in m ? ${cel.oneOrMany "m.name"} : []).flatten()).join(" ").replace("\t", " ") + " "))'';
}
