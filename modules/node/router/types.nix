# The option types and CEL the router kinds share: interfaces, address ranges, addresses and
# families, the names the Network documents match, what the rules of FirewallPolicy and
# PolicyRoute documents match, and NAT64 prefixes.
{ lib }:
let
  inherit (lib) mkOption types;
  inherit (lib.chalkos.types) portRange prefix ref;
  inherit (lib.chalkos.internal.api) apiVersions;
  inherit (lib.chalkos.internal) cel;
  # The documents of a router kind in the delivered config, by name.
  delivered = kind: cel.documents "config" apiVersions.router kind;
  family = types.enum [
    "ipv4"
    "ipv6"
  ];
  # A firewall rule's source or destination: an AddressSet or address ranges, one of them, which a
  # rule of its own checks; null matches every address.
  addresses = types.nullOr (
    types.submodule {
      options = {
        set = mkOption {
          type = types.nullOr (ref "AddressSet");
          default = null;
          example = "servers";
          description = "AddressSet whose ranges match; excludes `cidrs`.";
        };
        cidrs = mkOption {
          type = types.nullOr (types.nonEmptyListOf prefix);
          default = null;
          example = [ "2001:db8:1::/48" ];
          description = "Address ranges that match; excludes `set`. In each family the rule matches the ranges of that family.";
        };
      };
    }
  );
  # Whether the source or destination m leaves the family f: it is null, its AddressSet is of f,
  # or it holds a range of f. One that names neither a set nor ranges has a rule of its own. The
  # reference check has found the set, so document() returns it.
  leaves =
    m: f:
    ''(${m} == null || (${m}.set != null ? document("AddressSet", ${m}.set).spec.family == "${f}" : ${m}.cidrs == null || ${m}.cidrs.exists(a, cidr(a).ip(0).family() == "${f}")))'';
  # Whether the firewall rule r matches connections of the family f: its family, its protocol and
  # both its source and its destination leave f.
  matchesFamily =
    f:
    let
      otherICMP = if f == "ipv4" then "icmpv6" else "icmp";
    in
    ''((r.family == null || r.family == "${f}") && r.protocol != "${otherICMP}" && ${leaves "r.source" f} && ${leaves "r.destination" f})'';
  # Whether the source or destination m is of the rule's family alone.
  ofFamily =
    m:
    ''(${m} == null || (${m}.set != null ? document("AddressSet", ${m}.set).spec.family == r.family : ${m}.cidrs == null || ${m}.cidrs.all(a, cidr(a).ip(0).family() == r.family)))'';
  # The rule that a firewall or policy routing rule's source and destination each name an
  # AddressSet or ranges, one of them.
  oneOfSetAndCIDRs = {
    description = "A rule's source and destination each name an AddressSet or address ranges, one of them.";
    ok = "[r.source, r.destination].all(m, m == null || (m.set == null) != (m.cidrs == null))";
    message = "a rule's source or destination names both set and cidrs, or neither; give one, or leave the field out to match every address";
  };
in
{
  inherit delivered family;
  # An interface's name, or a glob of names: a prefix and `*`, the one wildcard nftables matches.
  interface = types.strMatching "[A-Za-z0-9_.:@-]{1,15}|[A-Za-z0-9_.:@-]{0,14}[*]";
  interfaceText = "by name or glob, a glob being a prefix followed by `*`";
  # An interface's name alone.
  interfaceName = types.strMatching "[A-Za-z0-9_.:@-]{1,15}";
  # An address range, which the rules read with cidr(), and an address, which they read with ip().
  inherit prefix;
  inherit (lib.chalkos.types) address;
  # A reference to a VRF document by its name, or default for the default VRF, which holds every
  # link no VRF holds and has no document.
  vrfRef = ref {
    kind = "VRF";
    reserved = [ "default" ];
  };
  # A reference to a FirewallZone document by its name.
  zoneRef = ref "FirewallZone";
  # A VRF document's name. The default VRF has no device and no table of a VRF document, so a zone
  # and a policy route name VRF documents alone.
  vrfDocRef = ref "VRF";
  inherit
    matchesFamily
    ofFamily
    oneOfSetAndCIDRs
    ;
  # What a FirewallPolicy's rules and a PolicyRoute's rules match.
  matchOptions = {
    family = mkOption {
      type = types.nullOr family;
      default = null;
      description = "Address family the rule matches; null matches each family its protocol and addresses leave.";
    };
    protocol = mkOption {
      type = types.nullOr (
        types.enum [
          "tcp"
          "udp"
          "sctp"
          "icmp"
          "icmpv6"
          "gre"
          "esp"
          "ah"
        ]
      );
      default = null;
      description = "Protocol the rule matches; null matches every protocol. `icmp` is IPv4's and `icmpv6` IPv6's.";
    };
    sourcePorts = mkOption {
      type = types.listOf portRange;
      default = [ ];
      example = [ "1024-65535" ];
      description = "Source ports, each a port or a range `<first>-<last>`, with the protocol `tcp`, `udp` or `sctp`; empty matches every port.";
    };
    destinationPorts = mkOption {
      type = types.listOf portRange;
      default = [ ];
      example = [
        22
        "8000-8100"
      ];
      description = "Destination ports, each a port or a range `<first>-<last>`, with the protocol `tcp`, `udp` or `sctp`; empty matches every port.";
    };
    source = mkOption {
      type = addresses;
      default = null;
      example.cidrs = [ "2001:db8:1::/48" ];
      description = "Source addresses: the ranges of an AddressSet in `set`, or address ranges in `cidrs`; null matches every source.";
    };
    destination = mkOption {
      type = addresses;
      default = null;
      example.set = "servers";
      description = "Destination addresses: the ranges of an AddressSet in `set`, or address ranges in `cidrs`; null matches every destination.";
    };
  };
  # The names and globs of Name= in the [Match] sections of the Network documents of the config c,
  # each value, or each of a list, as one string between spaces: systemd splits a value at spaces
  # and tabs. A list of names would cost a loop more than a string.
  matchNames =
    c:
    let
      networks = cel.documents c apiVersions.networkd "Network";
    in
    ''cel.bind(nets, ${networks}, cel.bind(matches, dyn(nets.map(n, "match" in nets[n].spec ? ${cel.oneOrMany "nets[n].spec.match"} : []).flatten()), " " + dyn(matches.map(m, "name" in m ? ${cel.oneOrMany "m.name"} : []).flatten()).join(" ").replace("\t", " ") + " "))'';

  # The rule that the field p holds a NAT64 prefix, as RFC 6052 gives them and Jool and Unbound
  # take them: an IPv6 range of one of its lengths, no IPv4-mapped one, of no link-local,
  # multicast or unspecified addresses, whose bits 64 to 71 are zero. Those bits are zero when
  # masking the first address to 72 bits gives what masking it to 64 bits gives.
  nat64Prefix =
    p:
    let
      range = ''cidr(${p}).ip(0).family() == "ipv6" && cidr(${p}).prefix() in [32, 40, 48, 56, 64, 96] && !cidr("::ffff:0:0/96").contains(cidr(${p}).ip(0))'';
      routable = ''cel.bind(x, cidr(${p}).ip(0), x != ip("::") && !cidr("fe80::/10").contains(x) && !cidr("ff00::/8").contains(x))'';
    in
    {
      description = "A NAT64 prefix is an IPv6 range of 32, 40, 48, 56, 64 or 96 bits of no link-local, multicast or unspecified addresses, whose bits 64 to 71 are zero, as RFC 6052 gives them.";
      rule = ''${range} && ${routable} && cel.bind(a, string(cidr(${p}).ip(0)), cidr(a + "/72").ip(0) == cidr(a + "/64").ip(0))'';
      messageExpression = ''!(${range}) ? "the prefix " + ${p} + " is no NAT64 prefix; give an IPv6 range of 32, 40, 48, 56, 64 or 96 bits, such as 64:ff9b::/96" : !${routable} ? "the prefix " + ${p} + " holds link-local, multicast or unspecified addresses" : "the prefix " + ${p} + " sets bits 64 to 71, which RFC 6052 keeps zero"'';
    };
}
