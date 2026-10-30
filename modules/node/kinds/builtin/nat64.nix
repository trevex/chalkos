# NAT64 and DNS on images with the router layer: the router's stateful NAT64 instances, each in a
# network namespace of its own joined to its VRF, which the output jool renders for Jool, and a DNS
# resolver with DNS64 per VRF, which the output unbound renders for Unbound
# (modules/node/router/nat64.nix and resolver.nix).
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists. So the
# rules over the pools of two NAT64 documents, or over two entries of one pool, loop over the
# pools' ranges flattened, the rules over the Network documents' static addresses bind their
# sections and their values as flat lists, and the rules over the local zones' records loop over
# the records flattened, without regular expressions.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin groups apiVersions;
  cel = import ../../../../lib/cel.nix { inherit lib; };
  inherit (import ../../../../lib/kinds/types.nix { inherit lib; }) portRange documentName;
  inherit (import ./router-types.nix { inherit lib; })
    prefix
    address
    vrfOf
    vrfDeclared
    ;

  # A VRF document's name, as a document's vrf gives it.
  vrfName = types.strMatching documentName;
  # A NAT64's link n64-<name> is an interface's name of 15 characters.
  maxName = 11;

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
  # Whether the address a is one a service listens on or sends to: no unspecified, link-local,
  # loopback, multicast or IPv4-mapped address.
  unicast =
    a:
    ''cel.bind(x, ip(${a}), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("169.254.0.0/16").contains(x) && !cidr("fe80::/10").contains(x) && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x) && !cidr("::ffff:0:0/96").contains(x))'';
  # The rule that the addresses, or with cidr the ranges, of the field self appear once each, in
  # whatever spelling; what names one in the message.
  once = parse: what: {
    description = "Each ${what} appears once.";
    rule = "self.all(a, size(self.filter(b, ${parse}(b) == ${parse}(a))) == 1)";
    messageExpression = ''"the ${what} " + self.filter(a, size(self.filter(b, ${parse}(b) == ${parse}(a))) > 1)[0] + " appears more than once"'';
  };
  # The first and last port of the pool entry a, whose ports are a port or a range "<first>-<last>".
  firstPort = a: ''(type(${a}.ports) == int ? ${a}.ports : int(${a}.ports.split("-")[0]))'';
  lastPort = a: ''(type(${a}.ports) == int ? ${a}.ports : int(${a}.ports.split("-")[1]))'';
  # A DNS name in lower case: labels of letters, digits, dashes and underscores, without the root's
  # dot.
  dnsName = types.strMatching "([a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?[.])*[a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?";

  # The pool's entries, self, flattened to [protocol, prefix, first port, last port] per protocol.
  poolRanges = ''self.map(a, ["tcp", "udp", "icmp"].filter(p, p in a.protocols).map(p, [p, a.prefix, ${firstPort "a"}, ${lastPort "a"}])).flatten()'';
  # Whether the pool ranges x and y share a protocol, addresses and ports. A range shares them
  # with itself, unless its ports are the wrong way round, which a rule of its own refuses.
  sharePool =
    x: y:
    "${x}[0] == ${y}[0] && (cidr(${x}[1]).contains(cidr(${y}[1]).ip(0)) || cidr(${y}[1]).contains(cidr(${x}[1]).ip(0))) && ${x}[2] <= ${y}[3] && ${y}[2] <= ${x}[3]";
  # A pool entry's prefix holds addresses a connection leaves from: none of them unspecified,
  # loopback, multicast or broadcast.
  sendable = ''cel.bind(c, cidr(a.prefix), c.ip(0).family() != "ipv4" || c.ip(0) != ip("0.0.0.0") && !cidr("127.0.0.0/8").contains(c.ip(0)) && !cidr("224.0.0.0/4").contains(c.ip(0)) && !c.contains(ip("255.255.255.255")))'';

  # The records of the local zones, self, flattened. A loop over them costs as much as two nested
  # loops, so their rules leave out regular expressions, and the output unbound checks the names
  # in the data of CNAME, PTR, MX and SRV records.
  records = "self.map(z, z.records).flatten()";
  # Whether the record r holds an address of its family if it is an A or AAAA record, which ip()
  # parses.
  recordAddress =
    r:
    ''(${r}.type == "A" ? ip(${r}.value).family() == "ipv4" : ${r}.type != "AAAA" || cel.bind(x, ip(${r}.value), x.family() == "ipv6" && !cidr("::ffff:0:0/96").contains(x)) && !${r}.value.contains("%"))'';
  # Whether the records x and y are one, an AAAA record's address in whatever spelling. The rule
  # that compares the records in pairs stays within chalkd's runtime cost limit for maxRecords
  # records, which a rule of its own keeps to.
  sameRecord =
    x: y:
    ''${x}.name == ${y}.name && ${x}.type == ${y}.type && (${x}.type == "AAAA" ? ip(${x}.value) == ip(${y}.value) : ${x}.value == ${y}.value)'';
  maxRecords = 150;
  fewOfMessage = 50;
  # Whether a local zone named z holds ipv4only.arpa or a name below it, which DNS64 answers.
  overIPv4Only =
    z:
    ''(${z} == "ipv4only.arpa" || ${z}.endsWith(".ipv4only.arpa") || "ipv4only.arpa".endsWith("." + ${z}))'';

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
      protocols = mkOption {
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

  localZone = types.submodule {
    options = {
      name = mkOption {
        type = dnsName;
        example = "lan.example.org";
        description = "Name of the zone, in lower case and without the root's dot.";
      };
      type = mkOption {
        type = types.enum [
          "static"
          "transparent"
          "typetransparent"
          "redirect"
          "refuse"
          "deny"
          "always_nxdomain"
        ];
        default = "static";
        description = "How Unbound answers names of the zone, as its `local-zone` types say: `static` answers from the records alone, `transparent` resolves the names the records lack, `typetransparent` also the types they lack, `redirect` answers every name below the zone with the zone's own records, `refuse` and `deny` refuse or drop the queries the records do not answer, and `always_nxdomain` answers that no name exists.";
      };
      records = mkOption {
        type = types.listOf (
          types.submodule {
            options = {
              name = mkOption {
                type = dnsName;
                example = "r1.lan.example.org";
                description = "Name of the record: the zone's name or a name below it.";
              };
              type = mkOption {
                type = types.enum [
                  "A"
                  "AAAA"
                  "CNAME"
                  "PTR"
                  "MX"
                  "SRV"
                  "TXT"
                ];
                description = "Type of the record.";
              };
              value = mkOption {
                type = types.strMatching "(]|[ !#-&(-[^-~]){1,255}";
                example = "2001:db8:1::1";
                description = "Data of the record, of up to 255 printable ASCII characters other than quotes and backslashes: an address for `A` and `AAAA`, a name for `CNAME` and `PTR`, `<preference> <name>` for `MX`, `<priority> <weight> <port> <name>` for `SRV`, and text for `TXT`.";
              };
              ttl = mkOption {
                type = types.ints.between 0 2147483647;
                default = 3600;
                description = "Seconds a resolver may keep the record.";
              };
            };
          }
        );
        default = [ ];
        description = "Records the router answers for names of the zone.";
      };
    };
  };
in
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.kinds = {
      NAT64 = builtin groups.router // {
        multiple = true;
        description = "A stateful NAT64 of the router, an instance of Jool in a network namespace of its own that a link joins to its VRF: it translates IPv6 connections to addresses of its prefix into IPv4 connections to the address each embeds, from the addresses of its pool.";
        rules = [
          {
            description = "A NAT64's name has ${toString maxName} characters at most, as its link n64-<name> is an interface's name of 15.";
            rule = "documents.all(n, size(n) <= ${toString maxName})";
            messageExpression = ''documents.filter(n, size(n) > ${toString maxName}).map(n, "NAT64 " + n).join(", ") + " has a name of more than ${toString maxName} characters, which its link n64-<name> does not take as an interface's name; give it a shorter one"'';
          }
          (vrfDeclared "NAT64")
          {
            description = "No two NAT64s of one VRF translate addresses of one prefix.";
            rule = "documents.all(n, documents.all(m, n == m || ${vrfOf "documents[n]"} != ${vrfOf "documents[m]"} || !(cidr(documents[n].spec.prefix).contains(cidr(documents[m].spec.prefix).ip(0)) || cidr(documents[m].spec.prefix).contains(cidr(documents[n].spec.prefix).ip(0)))))";
            messageExpression = ''documents.filter(n, documents.exists(m, n != m && ${vrfOf "documents[n]"} == ${vrfOf "documents[m]"} && (cidr(documents[n].spec.prefix).contains(cidr(documents[m].spec.prefix).ip(0)) || cidr(documents[m].spec.prefix).contains(cidr(documents[n].spec.prefix).ip(0))))).map(n, "NAT64 " + n).join(", ") + " translate addresses of one prefix in one VRF; give each NAT64 of a VRF a prefix of its own"'';
          }
          {
            description = "No two NAT64s share an address of their pools.";
            rule = "cel.bind(e, documents.map(n, documents[n].spec.pool4.map(a, [n, a.prefix])).flatten(), e.all(x, e.all(y, x[0] == y[0] || !(cidr(x[1]).contains(cidr(y[1]).ip(0)) || cidr(y[1]).contains(cidr(x[1]).ip(0))))))";
            messageExpression = ''cel.bind(e, documents.map(n, documents[n].spec.pool4.map(a, [n, a.prefix])).flatten(), e.filter(x, e.exists(y, x[0] != y[0] && (cidr(x[1]).contains(cidr(y[1]).ip(0)) || cidr(y[1]).contains(cidr(x[1]).ip(0))))).map(x, "NAT64 " + x[0] + " " + x[1]).join(", ") + " share addresses; give each NAT64 a pool of its own")'';
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
            type = types.nullOr vrfName;
            default = null;
            example = "tenant";
            description = "VRF document whose links route the prefix to the NAT64 and carry its translated connections; null for the default VRF. The NAT64 runs in the network namespace nat64-<name>, which the link n64-<name> joins to the VRF.";
          };
          prefix = lib.chalkos.mkOption {
            type = prefix;
            default = "64:ff9b::/96";
            description = "IPv6 range whose addresses the NAT64 translates, each embedding an IPv4 address as RFC 6052 says: 32, 40, 48, 56, 64 or 96 bits, by default the well-known prefix. The router routes it to the NAT64's link in the NAT64's VRF, a route that BGP announces when `BGP.spec.networks` of that VRF names the prefix.";
            rules = [ (nat64Prefix "self") ];
          };
          pool4 = lib.chalkos.mkOption {
            type = types.listOf poolEntry;
            default = [ ];
            example = [ { prefix = "192.0.2.10/32"; } ];
            description = "IPv4 addresses, with their ports, that the translated connections leave from: addresses routed to the router, not addresses of its links, which the router answers itself. The rules refuse an address that a `Network` document gives a link. An address a link takes from DHCP passes them, and `chalkos-nat64.service` then fails without changing an instance; leave pool4 empty instead. Empty takes the address of the link a connection leaves through, with the ports 61001 to 65535, which the router masquerades. The firewall's NAT rules leave the translated connections alone.";
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
                description = "A pool entry's ports lie from 1 to 65535 and leave out the kernel's ephemeral ports, 32768 to 60999.";
                rule = "self.all(a, cel.bind(lo, ${firstPort "a"}, cel.bind(hi, ${lastPort "a"}, lo >= 1 && lo <= hi && hi <= 65535 && (hi < 32768 || lo > 60999))))";
                messageExpression = ''"a pool entry takes the ports " + string(self.filter(a, !cel.bind(lo, ${firstPort "a"}, cel.bind(hi, ${lastPort "a"}, lo >= 1 && lo <= hi && hi <= 65535 && (hi < 32768 || lo > 60999))))[0].ports) + ", which lie outside 1-32767 and 61000-65535 or run backwards; Jool refuses the kernel's ephemeral ports, 32768 to 60999"'';
              }
              {
                description = "A pool entry names each protocol once.";
                rule = ''self.all(a, ["tcp", "udp", "icmp"].all(p, size(a.protocols.filter(q, q == p)) <= 1))'';
                message = "a pool entry names a protocol twice";
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

      Resolver = builtin groups.router // {
        multiple = true;
        description = "A DNS resolver of the router in one VRF, Unbound: the addresses it answers on, whom it answers, where it sends queries, and its DNS64 and local zones.";
        rules = [
          {
            description = "One Resolver serves a VRF.";
            rule = "cel.bind(v, documents.map(n, ${vrfOf "documents[n]"}), v.all(x, size(v.filter(y, y == x)) == 1))";
            messageExpression = ''cel.bind(v, documents.map(n, ${vrfOf "documents[n]"}), "more than one Resolver serves the VRF " + v.filter(x, size(v.filter(y, y == x)) > 1)[0] + "; a VRF has one resolver")'';
          }
          (vrfDeclared "Resolver")
          {
            description = "A Resolver listens on an address of a family its clients' ranges are of.";
            rule = ''documents.all(n, cel.bind(s, documents[n].spec, ["ipv4", "ipv6"].exists(f, s.listen.exists(a, ip(a).family() == f) && s.allow.exists(p, cidr(p).ip(0).family() == f))))'';
            messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, !["ipv4", "ipv6"].exists(f, s.listen.exists(a, ip(a).family() == f) && s.allow.exists(p, cidr(p).ip(0).family() == f)))).map(n, "Resolver " + n).join(", ") + " listens on no address of a family its clients' ranges are of, so it would answer no one; give listen and allow a family in common"'';
          }
          {
            description = "With DNS64, no local zone holds ipv4only.arpa, which Unbound would answer before DNS64.";
            rule = "documents.all(n, cel.bind(s, documents[n].spec, s.dns64 == null || !s.localZones.exists(z, ${overIPv4Only "z.name"})))";
            messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, s.dns64 != null && s.localZones.exists(z, ${overIPv4Only "z.name"}))).map(n, "Resolver " + n).join(", ") + " has a local zone over ipv4only.arpa, which Unbound would answer before DNS64; drop the zone or DNS64"'';
          }
        ];
        spec = {
          vrf = mkOption {
            type = types.nullOr vrfName;
            default = null;
            example = "tenant";
            description = "VRF document on whose links the resolver answers and through whose links it sends its queries; null for the default VRF.";
          };
          listen = lib.chalkos.mkOption {
            type = types.nonEmptyListOf address;
            example = [ "2001:db8:ffff::1" ];
            description = "Addresses of the router in the resolver's VRF on which it answers on port 53, over UDP and TCP; the firewall accepts DNS on them from `allow`.";
            rules = [
              {
                description = "The resolver listens on unicast addresses.";
                rule = "self.all(a, ${unicast "a"})";
                messageExpression = ''"the address " + self.filter(a, !${unicast "a"})[0] + " is no unicast address the resolver can listen on"'';
              }
              (once "ip" "address")
            ];
          };
          allow = lib.chalkos.mkOption {
            type = types.nonEmptyListOf prefix;
            example = [ "2001:db8::/32" ];
            description = "Address ranges of the clients the resolver answers; it refuses others, and the firewall accepts DNS from these alone. A family's whole range, `0.0.0.0/0` or `::/0`, answers whoever reaches the addresses of `listen`: an open resolver, if one of them faces the internet.";
            rules = [
              {
                description = "The resolver's clients are address ranges.";
                rule = "self.all(p, cidr(p).prefix() >= 0)";
                message = "a client is no address range";
              }
              (once "cidr" "range")
            ];
          };
          forwarders = lib.chalkos.mkOption {
            type = types.listOf address;
            default = [ ];
            example = [ "2001:db8::53" ];
            description = "Resolvers the router sends its queries to; empty resolves names itself, from the root servers.";
            rules = [
              {
                description = "The resolver forwards to unicast addresses.";
                rule = "self.all(a, ${unicast "a"})";
                messageExpression = ''"the forwarder " + self.filter(a, !${unicast "a"})[0] + " is no unicast address"'';
              }
              (once "ip" "forwarder")
            ];
          };
          dnssec = mkOption {
            type = types.bool;
            default = true;
            description = "Whether the resolver validates DNSSEC with the root's trust anchor of the image. The forwarders then pass DNSSEC records on.";
          };
          dns64 = lib.chalkos.mkOption {
            type = types.nullOr (
              types.submodule {
                options.prefix = mkOption {
                  type = prefix;
                  default = "64:ff9b::/96";
                  description = "NAT64 prefix the resolver synthesises AAAA records in, from the A records of names that have no AAAA record.";
                };
              }
            );
            default = null;
            example = {
              prefix = "64:ff9b::/96";
            };
            description = "DNS64: AAAA records the resolver synthesises for IPv4-only names, and its answer for `ipv4only.arpa`; null synthesises none.";
            rules = [
              (
                let
                  r = nat64Prefix "self.prefix";
                in
                r // { rule = "self == null || (${r.rule})"; }
              )
            ];
          };
          localZones = lib.chalkos.mkOption {
            type = types.listOf localZone;
            default = [ ];
            description = "Zones the resolver answers from its own records, ${toString maxRecords} records in all at most. With `dns64`, no zone holds `ipv4only.arpa`, which DNS64 answers.";
            rules = [
              {
                description = "A local zone appears once.";
                rule = "self.all(z, size(self.filter(y, y.name == z.name)) == 1)";
                messageExpression = ''"the local zone " + self.filter(z, size(self.filter(y, y.name == z.name)) > 1)[0].name + " appears more than once"'';
              }
              {
                description = "A local zone's records are of the zone's name or names below it.";
                rule = ''self.all(z, z.records.all(r, r.name == z.name || r.name.endsWith("." + z.name)))'';
                messageExpression = ''cel.bind(z, self.filter(z, !z.records.all(r, r.name == z.name || r.name.endsWith("." + z.name)))[0], "the local zone " + z.name + " holds a record of " + z.records.filter(r, !(r.name == z.name || r.name.endsWith("." + z.name)))[0].name + ", which is outside it")'';
              }
              {
                description = "The names of local zones and their records hold 253 characters at most.";
                rule = "self.all(z, size(z.name) <= 253) && ${records}.all(r, size(r.name) <= 253)";
                messageExpression = ''"the name " + (self.map(z, z.name) + ${records}.map(r, r.name)).filter(n, size(n) > 253)[0] + " holds more than 253 characters"'';
              }
              {
                description = "An A or AAAA record holds an address of its family.";
                rule = "${records}.all(r, ${recordAddress "r"})";
                messageExpression = ''cel.bind(r, ${records}.filter(r, !${recordAddress "r"})[0], "the " + r.type + " record of " + r.name + " holds " + r.value + ", which is no " + (r.type == "A" ? "IPv4" : "IPv6") + " address")'';
              }
              {
                description = "The local zones hold ${toString maxRecords} records at most, which the rules can compare in pairs.";
                rule = "size(${records}) <= ${toString maxRecords}";
                messageExpression = ''"the local zones hold " + string(size(${records})) + " records, more than the ${toString maxRecords} whose duplicates chalkd can check"'';
              }
              {
                description = "A record appears once, as Unbound keeps one of its copies.";
                rule = "cel.bind(e, ${records}, size(e) > ${toString maxRecords} || e.all(x, size(e.filter(y, ${sameRecord "x" "y"})) == 1))";
                # Naming the record compares every pair; past fewOfMessage records the message
                # says what is wrong alone.
                messageExpression = ''cel.bind(e, ${records}, size(e) > ${toString fewOfMessage} ? "a record appears more than once in the local zones, with one name, type and value; Unbound keeps one" : cel.bind(r, e.filter(x, size(e.filter(y, ${sameRecord "x" "y"})) > 1)[0], "the record " + r.name + " " + r.type + " " + (r.type == "AAAA" ? string(ip(r.value)) : r.value) + " appears more than once; Unbound keeps one"))'';
              }
              {
                description = "A redirect zone's records are of the zone's name, which Unbound answers for every name below it.";
                rule = ''self.all(z, z.type != "redirect" || z.records.all(r, r.name == z.name))'';
                messageExpression = ''cel.bind(z, self.filter(z, z.type == "redirect" && !z.records.all(r, r.name == z.name))[0], "the redirect zone " + z.name + " holds a record of " + z.records.filter(r, r.name != z.name)[0].name + "; a redirect zone's records are of its own name, which Unbound answers for every name below it")'';
              }
              {
                description = "A CNAME record's name holds no other record.";
                rule = ''cel.bind(e, ${records}, size(e) > ${toString maxRecords} || e.all(x, x.type != "CNAME" || size(e.filter(y, y.name == x.name)) == 1))'';
                messageExpression = ''cel.bind(e, ${records}, e.filter(x, x.type == "CNAME" && size(e.filter(y, y.name == x.name)) > 1)[0].name + " holds a CNAME record and another record; a CNAME's name holds it alone")'';
              }
            ];
          };
        };
      };
    };
  };
}
