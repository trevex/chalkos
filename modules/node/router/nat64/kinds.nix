# NAT64 on router images whose chalkos.router.nat64.enable is true: the router's stateful NAT64
# instances, each in a network namespace of its own joined to its VRF, which the output jool
# renders for Jool (default.nix).
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists. So the
# rule over two entries of one pool loops over the pool's ranges flattened, and the rules over the
# Network documents' static addresses bind their sections and their values as flat lists. What
# compares the documents of the kind in pairs the validator router checks: two NAT64s of one VRF
# whose prefixes overlap and two NAT64s whose pools share an address.
{ lib }:
let
  inherit (lib) mkOption types;
  inherit (lib.chalkos.internal) builtin;
  inherit (lib.chalkos.internal.api) groups apiVersions;
  inherit (lib.chalkos.internal) cel;
  inherit (lib.chalkos.types) portRange;
  inherit (import ../types.nix { inherit lib; })
    prefix
    address
    vrfRef
    nat64Prefix
    ;

  # A NAT64's link n64-<name> is an interface's name of 15 characters.
  maxName = 11;

  # The first and last port of the pool entry a, whose ports are a port or a range "<first>-<last>"
  # that the schema's format has checked.
  firstPort = a: "portRange(${a}.ports).first";
  lastPort = a: "portRange(${a}.ports).last";

  # The pool's entries, self, flattened to [protocol, prefix, first port, last port] per protocol.
  poolRanges = ''self.map(a, ["tcp", "udp", "icmp"].filter(p, p in a.protocols).map(p, [p, a.prefix, ${firstPort "a"}, ${lastPort "a"}])).flatten()'';
  # Whether the pool ranges x and y share a protocol, addresses and ports. A range shares them
  # with itself, as the schema's format keeps its ports in order.
  sharePool =
    x: y:
    "${x}[0] == ${y}[0] && (cidr(${x}[1]).contains(cidr(${y}[1]).ip(0)) || cidr(${y}[1]).contains(cidr(${x}[1]).ip(0))) && ${x}[2] <= ${y}[3] && ${y}[2] <= ${x}[3]";
  # A pool entry's prefix holds addresses a connection leaves from: none of them unspecified,
  # loopback, multicast or broadcast.
  sendable = ''cel.bind(c, cidr(a.prefix), c.ip(0).family() != "ipv4" || c.ip(0) != ip("0.0.0.0") && !cidr("127.0.0.0/8").contains(c.ip(0)) && !cidr("224.0.0.0/4").contains(c.ip(0)) && !c.contains(ip("255.255.255.255")))'';

  # An IPv4 address, with or without a length, as chalkos-render reads a link's address: four
  # numbers of 0 to 255 without leading zeros, which ip() parses.
  ipv4Text = "^(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])([.](25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])){3}(/(3[0-2]|[12]?[0-9]))?$";
  # Binds, for body, statics to the static IPv4 addresses the Network documents give their links,
  # without their lengths: the values of Address= in the [Network] and [Address] sections and of
  # ServerAddress= in [DHCPServer], a value or a list of them, that are IPv4 addresses once the
  # spaces around them go, as chalkos-render reads them. The sections and the values are each
  # bound as one list before the next loop, which keeps each loop within two nested ones; naming
  # each address's Network would cost a loop more, so the messages leave the Network out.
  staticsBy =
    body:
    let
      sections =
        key:
        "nets.map(n, has(nets[n].spec.${key}) ? ${cel.oneOrMany "nets[n].spec.${key}"} : []).flatten()";
      values = key: list: "${list}.map(s, has(s.${key}) ? ${cel.oneOrMany "s.${key}"} : []).flatten()";
    in
    ''cel.bind(nets, ${
      cel.documents "config" apiVersions.networkd "Network"
    }, cel.bind(net, ${sections "network"}, cel.bind(addr, ${sections "address"}, cel.bind(dhcp, ${sections "dhcpServer"}, cel.bind(values, dyn(${values "address" "net"} + ${values "address" "addr"} + ${values "serverAddress" "dhcp"}), cel.bind(statics, values.filter(v, type(v) == string && v.trim().matches("${ipv4Text}")).map(v, v.trim().split("/")[0]), ${body}))))))'';
  # The prefixes of every NAT64 document's pool.
  poolPrefixes = "documents.map(n, documents[n].spec.pool4.map(a, a.prefix)).flatten()";
  # Whether the pool prefix x holds the address a of statics.
  holds = x: a: "cidr(${x}).contains(ip(${a}))";
  linkRange = "169.254.64.0/18";

  poolEntry = types.submodule {
    options = {
      prefix = mkOption {
        type = prefix;
        example = "192.0.2.10/32";
        description = "IPv4 addresses the translated connections leave from, a range of 65536 addresses at most.";
      };
      ports = mkOption {
        type = portRange;
        default = "61001-65535";
        description = "Ports, or ICMP identifiers, of the addresses the translated connections take: a port or a range `<first>-<last>` that leaves out the kernel's ephemeral ports, 32768 to 60999, which the router's own connections take.";
      };
      protocols = lib.chalkos.mkOption {
        unique = {
          across = "list";
          hint = "name each protocol once";
        };
        type = types.nonEmptyListOf (
          types.enum [
            "tcp"
            "udp"
            "icmp"
          ]
        );
        default = [
          "tcp"
          "udp"
          "icmp"
        ];
        description = "Protocols whose connections take the addresses and ports.";
      };
    };
  };

in
{
  NAT64 = builtin groups.router // {
    multiple = true;
    description = "A stateful NAT64 of the router, an instance of Jool in a network namespace of its own that a link joins to its VRF: it translates IPv6 connections to addresses of its prefix into IPv4 connections to the address each embeds, from the addresses of its pool.";
    rules = [
      {
        description = "A NAT64's name has ${toString maxName} characters at most, as its link n64-<name> is an interface's name of 15.";
        rule = "documents.all(n, size(n) <= ${toString maxName})";
        messageExpression = ''documents.filter(n, size(n) > ${toString maxName}).map(n, "NAT64 " + n).join(", ") + " has a name of more than ${toString maxName} characters, which its link n64-<name> does not take as an interface's name; give it a shorter one"'';
      }
      {
        description = "No NAT64's pool holds a static address a Network document gives a link, which the router answers itself.";
        rule = "cel.bind(e, ${poolPrefixes}, ${staticsBy "e.all(x, statics.all(a, !${holds "x" "a"}))"})";
        messageExpression = "cel.bind(e, ${poolPrefixes}, ${staticsBy ''cel.bind(p, e.filter(x, statics.exists(a, ${holds "x" "a"}))[0], "NAT64 " + documents.filter(n, documents[n].spec.pool4.exists(a, a.prefix == p))[0] + ": the pool entry " + string(cidr(p)) + " holds " + string(ip(statics.filter(a, ${holds "p" "a"})[0])) + ", an address a Network document gives a link; the router answers it itself, so leave pool4 empty to take the address of the link the connections leave through")''})";
      }
      {
        description = "Beside NAT64s, no Network document gives a link a static address in ${linkRange}, the range of the NAT64s' links.";
        rule = "size(documents) == 0 || ${staticsBy ''statics.all(a, !cidr("${linkRange}").contains(ip(a)))''}";
        messageExpression = staticsBy ''"a Network document gives a link the address " + string(ip(statics.filter(a, cidr("${linkRange}").contains(ip(a)))[0])) + ", which lies in ${linkRange}, the range of the NAT64s' links; give the link another address"'';
      }
    ];
    spec = {
      vrf = mkOption {
        type = vrfRef;
        default = "default";
        example = "tenant";
        description = "VRF document whose links route the prefix to the NAT64 and carry its translated connections; `default` for the default VRF. The NAT64 runs in the network namespace nat64-<name>, which the link n64-<name> joins to the VRF.";
      };
      prefix = lib.chalkos.mkOption {
        type = prefix;
        default = "64:ff9b::/96";
        description = "IPv6 range whose addresses the NAT64 translates, each embedding an IPv4 address as RFC 6052 says: 32, 40, 48, 56, 64 or 96 bits, by default the well-known prefix. The router routes it to the NAT64's link in the NAT64's VRF, a route that BGP announces when `BGP.spec.networks` of that VRF names the prefix. The prefixes of the NAT64s of one VRF do not overlap, which the validator `router` checks.";
        rules = [ (nat64Prefix "self") ];
      };
      pool4 = lib.chalkos.mkOption {
        type = types.listOf poolEntry;
        default = [ ];
        example = [ { prefix = "192.0.2.10/32"; } ];
        description = "IPv4 addresses, with their ports, that the translated connections leave from: addresses routed to the router, not addresses of its links, which the router answers itself. The rules refuse an address that a `Network` document gives a link. An address a link takes from DHCP passes them, and `chalkos-nat64.service` then fails without changing an instance; leave pool4 empty instead. Empty takes the address of the link a connection leaves through, with the ports 61001 to 65535, which the router masquerades. The pools of two NAT64s share no address, which the validator `router` checks. The firewall's NAT rules leave the translated connections alone.";
        rules = [
          {
            description = "A pool's entries are IPv4 ranges of 65536 addresses at most.";
            rule = ''self.all(a, cidr(a.prefix).ip(0).family() == "ipv4" && cidr(a.prefix).prefix() >= 16)'';
            messageExpression = ''"the pool entry " + self.filter(a, !(cidr(a.prefix).ip(0).family() == "ipv4" && cidr(a.prefix).prefix() >= 16))[0].prefix + " is no IPv4 range of 16 bits or more"'';
          }
          {
            description = "A pool's entries hold no unspecified, loopback, multicast or broadcast address.";
            rule = "self.all(a, ${sendable})";
            messageExpression = ''"the pool entry " + self.filter(a, !${sendable})[0].prefix + " holds addresses no connection leaves from: the unspecified address, loopback, multicast or broadcast"'';
          }
          {
            description = "A pool's entries hold no link-local address; the NAT64s' links take 169.254.64.0/18.";
            rule = ''self.all(a, !cidr("169.254.0.0/16").contains(cidr(a.prefix).ip(0)))'';
            messageExpression = ''"the pool entry " + self.filter(a, cidr("169.254.0.0/16").contains(cidr(a.prefix).ip(0)))[0].prefix + " holds link-local addresses, which the NAT64s' links take"'';
          }
          {
            description = "A pool entry's ports leave out the kernel's ephemeral ports, 32768 to 60999.";
            rule = "self.all(a, ${lastPort "a"} < 32768 || ${firstPort "a"} > 60999)";
            messageExpression = ''"a pool entry takes the ports " + string(self.filter(a, !(${lastPort "a"} < 32768 || ${firstPort "a"} > 60999))[0].ports) + ", which touch the kernel's ephemeral ports, 32768 to 60999, which Jool refuses; give ports from 1 to 32767 or from 61000 to 65535"'';
          }
          {
            description = "No two entries of a pool share a protocol, addresses and ports.";
            rule = "cel.bind(e, ${poolRanges}, e.all(x, size(e.filter(y, ${sharePool "x" "y"})) <= 1))";
            messageExpression = ''cel.bind(e, ${poolRanges}, cel.bind(x, e.filter(x, size(e.filter(y, ${sharePool "x" "y"})) > 1)[0], "the pool entry " + x[1] + " shares " + x[0] + " addresses and ports with another; give each entry of a protocol addresses or ports of its own"))'';
          }
        ];
      };
    };
  };
}
