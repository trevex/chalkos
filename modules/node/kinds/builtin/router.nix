# How a router forwards and filters, on images with the router layer: the families it forwards,
# its reverse-path filter, its multipath hash and the interfaces of its node API, and the
# firewall's zones, policies, address sets and NAT rules, which the outputs router-sysctl and
# router-nftables render (modules/node/router).
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists. So a rule
# over a policy's firewall rules sits on spec.rules, where document() looks up a rule's AddressSet
# at a constant cost, and lists of lists are flattened before the next loop reads them.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin groups apiVersions;
  inherit (import ../../../../lib/kinds/types.nix { inherit lib; }) portRange ref;
  inherit (import ./router-types.nix { inherit lib; })
    interface
    interfaceText
    interfaceName
    prefix
    address
    family
    matchNames
    ;
  cel = import ../../../../lib/cel.nix { inherit lib; };

  zoneRef = ref "FirewallZone";
  # A FirewallZone document's name, or local for the router itself, which has no document.
  localRef = ref {
    kind = "FirewallZone";
    reserved = [ "local" ];
  };
  # A VRF document's name. The default VRF has no device and no table of a VRF document, so a zone
  # and a policy route name VRF documents alone.
  vrfDocRef = ref "VRF";
  # Binds, for body, all to the interfaces of every FirewallZone as one list, which a rule loops
  # over rather than over each zone's interfaces: nested loops, or a loop over a list flattened in
  # place, would cost more than chalkd allows.
  zoneInterfaces =
    body: "cel.bind(all, documents.map(n, documents[n].spec.interfaces).flatten(), ${body})";
  # The places out and in of the NATRule n.
  natPlaces = ''[documents[n].spec.out, documents[n].spec["in"]]'';
  # Whether the place p of a NATRule names the device of a VRF of vrfs.
  natDevice = "p != null && p.interface != null && p.interface in vrfs";
  verdict = types.enum [
    "accept"
    "drop"
    "reject"
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

  # The documents of a router kind in the delivered config, by name.
  delivered = kind: cel.documents "config" apiVersions.router kind;
  # A rule over a FirewallPolicy's firewall rules, self: `ok` holds for each firewall rule r.
  rulesRule =
    {
      description,
      ok,
      message,
    }:
    {
      inherit description message;
      rule = "self.all(r, ${ok})";
    };
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

  # Whether the names s, from matchNames, name the management interface i: they hold i, a name
  # that the glob i's prefix starts, any name for the glob `*`, or a glob whose prefix starts i,
  # which globs, the alternatives of a regular expression, matches.
  names =
    s:
    ''(${s}.contains(" " + i + " ") || i.endsWith("*") && (size(i) > 1 ? ${s}.contains(" " + i.substring(0, size(i) - 1)) : ${s}.trim() != "") || ${s}.matches(" (" + globs + ")[*] "))'';
  # The running management interfaces, oldSelf, that a running Network document names and no
  # delivered one does, bound to lost around expr. globs holds the prefixes of i's name, from the
  # empty one to the whole of a name and to the `*` of a glob, with dots escaped.
  bindLost =
    expr:
    ''cel.bind(was, ${matchNames "oldConfig"}, cel.bind(gets, ${matchNames "config"}, cel.bind(lost, oldSelf.filter(i, cel.bind(globs, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15].filter(k, k < size(i) || k == size(i) && !i.endsWith("*")).map(k, i.substring(0, k).replace(".", "[.]")).join("|"), ${names "was"} && !${names "gets"})), ${expr})))'';

  # Where a NAT rule's connections come in or go out: a zone or an interface.
  place = types.submodule {
    options = {
      zone = mkOption {
        type = types.nullOr zoneRef;
        default = null;
        description = "FirewallZone whose interfaces the connections use; excludes `interface`.";
      };
      interface = mkOption {
        type = types.nullOr interface;
        default = null;
        description = "Interface the connections use, ${interfaceText}; excludes `zone`.";
      };
    };
  };

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

  firewallRule = types.submodule {
    options = matchOptions // {
      icmpTypes = mkOption {
        type = types.listOf (
          types.enum [
            "echo-request"
            "echo-reply"
            "destination-unreachable"
            "time-exceeded"
            "parameter-problem"
            "packet-too-big"
          ]
        );
        default = [ ];
        description = "ICMP or ICMPv6 types, with the protocol `icmp` or `icmpv6`; empty matches every type. `packet-too-big` is ICMPv6's.";
      };
      action = mkOption {
        type = verdict;
        description = "What happens to a connection the rule matches; `reject` answers with an administratively prohibited error.";
      };
      log = mkOption {
        type = types.bool;
        default = false;
        description = "Whether the kernel logs each packet the rule matches, prefixed with the policy's name and the rule's number.";
      };
    };
  };

  # A rule over a PolicyRoute's rules, self: `ok` holds for each rule p. It binds p's match to r,
  # so that the firewall rules' CEL reads it.
  policyRulesRule =
    {
      description,
      ok,
      message,
    }:
    {
      inherit description message;
      rule = "self.all(p, cel.bind(r, p.match, ${ok}))";
    };
  # Binds, for body, tables to the VRF documents' tables.
  vrfTables =
    body: "cel.bind(v, ${delivered "VRF"}, cel.bind(tables, v.map(n, v[n].spec.table), ${body}))";
  # Binds, for body, all to the interfaces of every PolicyRoute's in as one list, as
  # zoneInterfaces does.
  policyInterfaces =
    body:
    ''cel.bind(all, documents.map(n, documents[n].spec["in"] == null ? [] : documents[n].spec["in"].interfaces).flatten(), ${body})'';

  # Where a PolicyRoute's rule sends the traffic it matches: a VRF, a table or a next hop.
  policyTarget = types.submodule {
    options = {
      vrf = mkOption {
        type = types.nullOr vrfDocRef;
        default = null;
        example = "transit";
        description = "VRF document whose routing table routes the traffic; excludes `table` and `nextHop`.";
      };
      table = mkOption {
        type = types.nullOr (types.ints.between 1 4294967295);
        default = null;
        example = 254;
        description = "Routing table that routes the traffic, by number: 254 is the main table. Not 255, the kernel's local table, and no VRF's table, which `vrf` names. A destination the table has no route to takes the route it would take without the policy; excludes `vrf` and `nextHop`.";
      };
      nextHop = mkOption {
        type = types.nullOr (
          types.submodule {
            options = {
              address = mkOption {
                type = address;
                example = "198.51.100.1";
                description = "Address of the router the traffic leaves through, of the family the rule matches.";
              };
              interface = mkOption {
                type = interfaceName;
                example = "wan1";
                description = "Link the next hop is on, which a Network document names alone in its `[Match]` section; the route goes into that Network.";
              };
              table = mkOption {
                type = types.ints.between 1 4294967295;
                example = 200;
                description = "Routing table of the next hop: a default route through it and an unreachable default route after it, so that the traffic is dropped while the link is down. One next hop's alone, and no rule's `table`, which the outputs check when they render the config, at an apply or a `--dry-run` but not in `chalkctl validate`; none of 253, 254 and 255 and no VRF's table.";
              };
            };
          }
        );
        default = null;
        description = "Router the traffic leaves through, by the default route of a table of its own; excludes `vrf` and `table`.";
      };
    };
  };

  policyRule = types.submodule {
    options = {
      match = mkOption {
        type = types.submodule {
          options = matchOptions // {
            dscp = mkOption {
              type = types.listOf (types.ints.between 0 63);
              default = [ ];
              example = [ 46 ];
              description = "DSCP values of the packets, each from 0 to 63; empty matches every value.";
            };
          };
        };
        example = {
          source.set = "guests";
          protocol = "tcp";
          destinationPorts = [ 443 ];
        };
        description = "What the rule matches, at least one term: a rule for every packet says so, such as with `source: {cidrs: [0.0.0.0/0]}`.";
      };
      to = mkOption {
        type = policyTarget;
        description = "Where the traffic the rule matches goes: `vrf`, `table` or `nextHop`.";
      };
    };
  };
in
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.kinds = {
      Router = builtin groups.router // {
        description = "How the router forwards: the families it forwards, its reverse-path filter, how it spreads flows over equal-cost routes, its connection table and the interfaces that reach its node API.";
        spec = {
          forwarding = {
            ipv4 = mkOption {
              type = types.bool;
              default = true;
              description = "Whether the router forwards IPv4 packets between its interfaces.";
            };
            ipv6 = mkOption {
              type = types.bool;
              default = true;
              description = "Whether the router forwards IPv6 packets between its interfaces.";
            };
          };
          rpFilter = mkOption {
            type = types.enum [
              "off"
              "loose"
              "strict"
            ];
            default = "loose";
            description = "Reverse-path filter of the packets the router receives, in both families: `strict` drops a packet whose source the router would not route back out of the interface it came in on, `loose` one whose source it has no route to, and `off` drops none. DHCPv4 between ports 67 and 68 passes.";
          };
          ecmpHash = mkOption {
            type = types.enum [
              "l3"
              "l4"
            ];
            default = "l4";
            description = "What the router hashes to pick one of several equal-cost routes for a flow, in both families: the addresses (`l3`), or the addresses, the protocol and the ports (`l4`).";
          };
          conntrackMax = mkOption {
            type = types.nullOr types.ints.positive;
            default = null;
            description = "Connections the router tracks at most, `net.netfilter.nf_conntrack_max`; null keeps the kernel's default, which follows the memory.";
          };
          management.interfaces = lib.chalkos.mkOption {
            type = types.nullOr (types.nonEmptyListOf interface);
            default = null;
            example = [ "eth0" ];
            description = "Interfaces, ${interfaceText}, on which the firewall accepts the node API, chalkd's TCP port 50000; null accepts it on every interface.";
            # A delivery that drops the Network of the interface an operator reaches the router
            # through cuts the operator off, unless it moves the node API too.
            rules = [
              {
                description = "A delivery that keeps the management interfaces keeps a Network naming each management interface that a running Network names, in a [Match] section's Name=; a glob names every interface its prefix starts, so with `*` a delivery keeps one Network at least.";
                rule = "self != oldSelf || oldSelf == null || ${bindLost "size(lost) == 0"}";
                messageExpression = bindLost ''"the config leaves the management interfaces " + lost.join(", ") + " without a Network document that matches them, so the router would no longer configure them; keep a Network that matches each, or change spec.management.interfaces in the same delivery"'';
              }
            ];
          };
        };
      };

      FirewallZone = builtin groups.router // {
        multiple = true;
        description = "A zone of the router's firewall: interfaces and VRFs whose connections the FirewallPolicy documents treat alike. The zone local is the router itself and has no document.";
        rules = [
          {
            description = "No FirewallZone is named local, the router itself.";
            rule = ''!("local" in documents)'';
            message = "a FirewallZone is named local, which is the router itself: FirewallPolicy documents name it without a document, so give the zone another name";
          }
          {
            description = "A FirewallZone names an interface or a VRF.";
            rule = "documents.all(n, size(documents[n].spec.interfaces) > 0 || size(documents[n].spec.vrfs) > 0)";
            messageExpression = ''documents.filter(n, size(documents[n].spec.interfaces) == 0 && size(documents[n].spec.vrfs) == 0).map(n, "FirewallZone " + n).join(", ") + ": a FirewallZone names an interface or a VRF; give it interfaces, vrfs or both"'';
          }
          {
            description = "No interface of a FirewallZone is a VRF's device, which takes the VRF document's name; the output router-nftables also refuses globs that match one.";
            rule = "cel.bind(vrfs, ${delivered "VRF"}, ${zoneInterfaces "all.all(i, !(i in vrfs))"})";
            messageExpression = "cel.bind(vrfs, ${delivered "VRF"}, ${zoneInterfaces ''cel.bind(i, all.filter(i, i in vrfs)[0], "FirewallZone " + documents.filter(n, i in documents[n].spec.interfaces)[0] + ": " + i + " is the device of VRF " + i + "; name the VRF in vrfs")''})";
          }
        ];
        spec = {
          # An interface or glob is in one FirewallZone, once, compared as written; the output
          # router-nftables refuses an interface that may be a link of a VRF another zone holds.
          interfaces = lib.chalkos.mkOption {
            type = types.listOf interface;
            unique.hint = "give each interface one zone";
            default = [ ];
            example = [
              "eth1"
              "vlan*"
            ];
            description = "Interfaces of the zone, ${interfaceText}. A link a VRF holds may be named here without its VRF, so that one VRF's links fall into several zones; the VRF itself is then in no other zone.";
          };
          vrfs = lib.chalkos.mkOption {
            type = types.listOf vrfDocRef;
            unique.hint = "give each VRF one zone";
            default = [ ];
            example = [ "tenant" ];
            description = "VRF documents whose links are all in the zone, those added later included; a VRF is in one zone at most.";
          };
        };
      };

      FirewallPolicy = builtin groups.router // {
        multiple = true;
        description = "What the router's firewall does with new connections from one zone to another, or to the router itself: rules in order, then a default.";
        rules = [
          {
            description = "A FirewallPolicy comes from a FirewallZone, not from local.";
            rule = ''documents.all(n, documents[n].spec.from != "local")'';
            messageExpression = ''documents.filter(n, documents[n].spec.from == "local").map(n, "FirewallPolicy " + n).join(", ") + " comes from local, the router itself, whose own connections the firewall accepts; a policy comes from a FirewallZone"'';
          }
        ];
        # One FirewallPolicy at most goes from a zone to another.
        unique = [
          {
            by = [
              "from"
              "to"
            ];
            hint = "give each pair of zones one policy";
          }
        ];
        spec = {
          # local is the router itself, which has no FirewallZone document: a rule refuses it
          # in from, where it would be a policy of the router's own connections.
          from = mkOption {
            type = localRef;
            description = "FirewallZone the connections come from.";
          };
          to = mkOption {
            type = localRef;
            description = "FirewallZone the connections go to, or `local` for connections to the router itself.";
          };
          default = mkOption {
            type = verdict;
            default = "drop";
            description = "What happens to a connection no rule matches; `reject` answers with an administratively prohibited error.";
          };
          rules = lib.chalkos.mkOption {
            type = types.listOf firewallRule;
            default = [ ];
            description = "Rules in order: the first that matches a connection decides.";
            rules = [
              (rulesRule oneOfSetAndCIDRs)
              (rulesRule {
                description = "A rule with a family has addresses and sets of that family alone.";
                ok = "r.family == null || ${ofFamily "r.source"} && ${ofFamily "r.destination"}";
                message = "a rule has addresses or a set of another family than its own";
              })
              (rulesRule {
                description = "A rule matches IPv4 or IPv6: its family, protocol, addresses and sets leave one.";
                ok = "${matchesFamily "ipv4"} || ${matchesFamily "ipv6"}";
                message = "a rule matches no connection: its family, protocol, addresses and sets leave neither IPv4 nor IPv6";
              })
              (rulesRule {
                description = "Ports go with tcp, udp or sctp, ICMP types with icmp or icmpv6, and packet-too-big with icmpv6.";
                ok = ''(size(r.sourcePorts) == 0 && size(r.destinationPorts) == 0 || r.protocol in ["tcp", "udp", "sctp"]) && (size(r.icmpTypes) == 0 || r.protocol in ["icmp", "icmpv6"]) && !(r.protocol == "icmp" && "packet-too-big" in r.icmpTypes)'';
                message = "a rule has ports without the protocol tcp, udp or sctp, or ICMP types its protocol lacks";
              })
            ];
          };
        };
      };

      AddressSet = builtin groups.router // {
        multiple = true;
        description = "Address ranges of one family, which the rules of FirewallPolicy and PolicyRoute documents name in a `source` or `destination` as `{set: <name>}`.";
        rules = [
          {
            description = "An AddressSet's ranges are of its family.";
            rule = "documents.all(n, documents[n].spec.prefixes.all(p, cidr(p).ip(0).family() == documents[n].spec.family))";
            messageExpression = ''documents.filter(n, !documents[n].spec.prefixes.all(p, cidr(p).ip(0).family() == documents[n].spec.family)).map(n, "AddressSet " + n + " holds ranges of another family than " + documents[n].spec.family).join("; ")'';
          }
        ];
        spec = {
          family = mkOption {
            type = family;
            description = "Address family of the set.";
          };
          prefixes = mkOption {
            type = types.nonEmptyListOf prefix;
            example = [ "2001:db8:1::/48" ];
            description = "Address ranges of the set.";
          };
        };
      };

      NATRule = builtin groups.router // {
        multiple = true;
        description = "An address translation of the router, which applies whether or not any FirewallPolicy exists: masquerade or source NAT of connections that leave, destination NAT of connections that arrive.";
        rules = [
          {
            description = "masquerade and snat name out alone, dnat names in alone.";
            rule = ''documents.all(n, cel.bind(s, documents[n].spec, s.type == "dnat" ? s["in"] != null && s.out == null : s.out != null && s["in"] == null))'';
            messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, !(s.type == "dnat" ? s["in"] != null && s.out == null : s.out != null && s["in"] == null))).map(n, "NATRule " + n).join(", ") + ": a masquerade or snat rule names out alone, a dnat rule in alone"'';
          }
          {
            description = "out and in name a zone or an interface, one of them.";
            rule = ''documents.all(n, [documents[n].spec.out, documents[n].spec["in"]].all(p, p == null || (p.zone == null) != (p.interface == null)))'';
            messageExpression = ''documents.filter(n, ![documents[n].spec.out, documents[n].spec["in"]].all(p, p == null || (p.zone == null) != (p.interface == null))).map(n, "NATRule " + n).join(", ") + ": out and in name a zone or an interface, one of them"'';
          }
          {
            description = "A NATRule's interface is no VRF's device, which takes the VRF document's name; the output router-nftables also refuses a glob that matches devices alone.";
            rule = "cel.bind(vrfs, ${delivered "VRF"}, documents.all(n, !${natPlaces}.exists(p, ${natDevice})))";
            messageExpression = ''cel.bind(vrfs, ${delivered "VRF"}, cel.bind(n, documents.filter(n, ${natPlaces}.exists(p, ${natDevice}))[0], cel.bind(i, ${natPlaces}.filter(p, ${natDevice})[0].interface, "NATRule " + n + ": " + i + " is the device of VRF " + i + "; name the VRF's links or a zone of the VRF")))'';
          }
          {
            description = "snat and dnat give to, masquerade does not.";
            rule = ''documents.all(n, (documents[n].spec.type == "masquerade") == (documents[n].spec.to == null))'';
            messageExpression = ''documents.filter(n, (documents[n].spec.type == "masquerade") != (documents[n].spec.to == null)).map(n, "NATRule " + n).join(", ") + ": snat and dnat translate to the address of to, masquerade to that of the interface and has no to"'';
          }
          {
            description = "A NATRule's addresses are of its family.";
            rule = "documents.all(n, cel.bind(s, documents[n].spec, (s.match.source + s.match.destination).all(a, cidr(a).ip(0).family() == s.family) && (s.to == null || ip(s.to.address).family() == s.family)))";
            messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, !((s.match.source + s.match.destination).all(a, cidr(a).ip(0).family() == s.family) && (s.to == null || ip(s.to.address).family() == s.family)))).map(n, "NATRule " + n + " has addresses of another family than " + documents[n].spec.family).join("; ")'';
          }
          {
            description = "Ports go with match.protocol.";
            rule = "documents.all(n, cel.bind(s, documents[n].spec, s.match.protocol != null || size(s.match.destinationPorts) == 0 && (s.to == null || s.to.port == null)))";
            messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, s.match.protocol == null && (size(s.match.destinationPorts) > 0 || s.to != null && s.to.port != null))).map(n, "NATRule " + n).join(", ") + ": ports need match.protocol"'';
          }
        ];
        spec = {
          type = mkOption {
            type = types.enum [
              "masquerade"
              "snat"
              "dnat"
            ];
            description = "`masquerade` translates the source of a leaving connection to the address of the interface it leaves through, `snat` to `to.address`, and `dnat` the destination of an arriving connection to `to.address`.";
          };
          family = mkOption {
            type = family;
            default = "ipv4";
            description = "Address family the rule translates.";
          };
          out = mkOption {
            type = types.nullOr place;
            default = null;
            description = "Where the connections leave, for `masquerade` and `snat`.";
          };
          "in" = mkOption {
            type = types.nullOr place;
            default = null;
            description = "Where the connections arrive, for `dnat`.";
          };
          match = {
            source = mkOption {
              type = types.listOf prefix;
              default = [ ];
              description = "Source address ranges of the connections; empty matches every source.";
            };
            destination = mkOption {
              type = types.listOf prefix;
              default = [ ];
              description = "Destination address ranges of the connections; empty matches every destination.";
            };
            protocol = mkOption {
              type = types.nullOr (
                types.enum [
                  "tcp"
                  "udp"
                  "sctp"
                ]
              );
              default = null;
              description = "Protocol of the connections; null matches every protocol.";
            };
            destinationPorts = mkOption {
              type = types.listOf portRange;
              default = [ ];
              description = "Destination ports, each a port or a range `<first>-<last>`, with `protocol`; empty matches every port.";
            };
          };
          to = mkOption {
            type = types.nullOr (
              types.submodule {
                options = {
                  address = mkOption {
                    type = address;
                    description = "Address the rule translates to.";
                  };
                  port = mkOption {
                    type = types.nullOr (types.ints.between 1 65535);
                    default = null;
                    description = "Port the rule translates to, with `match.protocol`; null keeps the port.";
                  };
                };
              }
            );
            default = null;
            description = "What `snat` and `dnat` translate to.";
          };
        };
      };

      PolicyRoute = builtin groups.router // {
        multiple = true;
        description = "Policy routing of the router: rules that send the forwarded traffic they match, coming in on a zone's links, on interfaces or on any link, to a VRF's routing table, another table or a next hop, whatever its destination's route. The first rule that matches decides; documents apply in the order of their names.";
        rules = [
          {
            description = "A PolicyRoute's in names a zone or interfaces, one of them.";
            rule = ''documents.all(n, [dyn(documents[n].spec["in"])].all(i, i == null || (i.zone == null) != (size(i.interfaces) == 0)))'';
            messageExpression = ''documents.filter(n, [dyn(documents[n].spec["in"])].exists(i, i != null && (i.zone == null) == (size(i.interfaces) == 0))).map(n, "PolicyRoute " + n).join(", ") + ": in names a zone or interfaces, one of them; leave in out for traffic from every link"'';
          }
          {
            description = "No interface of a PolicyRoute's in is a VRF's device, which takes the VRF document's name; the outputs also refuse globs that match one.";
            rule = "cel.bind(vrfs, ${delivered "VRF"}, ${policyInterfaces "all.all(i, !(i in vrfs))"})";
            messageExpression = "cel.bind(vrfs, ${delivered "VRF"}, ${policyInterfaces ''cel.bind(i, all.filter(i, i in vrfs)[0], "PolicyRoute " + documents.filter(n, documents[n].spec["in"] != null && i in documents[n].spec["in"].interfaces)[0] + ": " + i + " is the device of VRF " + i + "; name the VRF's links, or a zone of the VRF")''})";
          }
        ];
        spec = {
          "in" = mkOption {
            type = types.nullOr (
              types.submodule {
                options = {
                  zone = mkOption {
                    type = types.nullOr zoneRef;
                    default = null;
                    description = "FirewallZone whose links the traffic comes in on; excludes `interfaces`.";
                  };
                  interfaces = mkOption {
                    type = types.listOf interface;
                    default = [ ];
                    example = [
                      "lan0"
                      "vlan*"
                    ];
                    description = "Interfaces the traffic comes in on, ${interfaceText}; excludes `zone`.";
                  };
                };
              }
            );
            default = null;
            description = "Where the traffic comes in: a zone or interfaces; null for every link.";
          };
          rules = lib.chalkos.mkOption {
            type = types.nonEmptyListOf policyRule;
            description = "Rules in order: the first that matches a packet decides where it goes.";
            rules = [
              (policyRulesRule oneOfSetAndCIDRs)
              (policyRulesRule {
                description = "A rule with a family has addresses and sets of that family alone.";
                ok = "r.family == null || ${ofFamily "r.source"} && ${ofFamily "r.destination"}";
                message = "a rule has addresses or a set of another family than its own";
              })
              (policyRulesRule {
                description = "A rule matches IPv4 or IPv6: its family, protocol, addresses and sets leave one.";
                ok = "${matchesFamily "ipv4"} || ${matchesFamily "ipv6"}";
                message = "a rule matches no packet: its family, protocol, addresses and sets leave neither IPv4 nor IPv6";
              })
              (policyRulesRule {
                description = "Ports go with tcp, udp or sctp.";
                ok = ''size(r.sourcePorts) == 0 && size(r.destinationPorts) == 0 || r.protocol in ["tcp", "udp", "sctp"]'';
                message = "a rule has ports without the protocol tcp, udp or sctp";
              })
              (policyRulesRule {
                description = "A rule matches by one term at least.";
                ok = "r.family != null || r.protocol != null || r.source != null || r.destination != null || size(r.sourcePorts) > 0 || size(r.destinationPorts) > 0 || size(r.dscp) > 0";
                message = "a rule has an empty match, which would take every packet; to send every packet, say so, such as with source: {cidrs: [0.0.0.0/0]}";
              })
              (policyRulesRule {
                description = "A rule's to names a VRF, a table or a next hop, one of them.";
                ok = "(p.to.vrf == null ? 0 : 1) + (p.to.table == null ? 0 : 1) + (p.to.nextHop == null ? 0 : 1) == 1";
                message = "a rule's to names a VRF, a table or a next hop, one of them";
              })
              (policyRulesRule {
                description = "A rule with a next hop matches packets of the next hop's family.";
                ok = ''p.to.nextHop == null || (ip(p.to.nextHop.address).family() == "ipv4" ? ${matchesFamily "ipv4"} : ${matchesFamily "ipv6"})'';
                message = "a rule's next hop is of another family than the packets the rule matches";
              })
              {
                description = "A rule's table is not 255, the kernel's local table, and no VRF's table.";
                rule = vrfTables "self.all(p, p.to.table == null || p.to.table != 255 && !(p.to.table in tables))";
                message = "a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table";
              }
              {
                description = "A next hop's table is none of 253, 254 and 255 and no VRF's table.";
                rule = vrfTables "self.all(p, p.to.nextHop == null || !(p.to.nextHop.table in [253, 254, 255]) && !(p.to.nextHop.table in tables))";
                message = "a next hop's table is 253, 254 or 255, the kernel's own, or a VRF's table; give the next hop a table of its own";
              }
              {
                description = "A next hop's interface is not lo; the output router-nftables also refuses it.";
                rule = ''self.all(p, p.to.nextHop == null || p.to.nextHop.interface != "lo")'';
                message = "a next hop's interface is lo, which no next hop is on; name the link the next hop is on";
              }
            ];
          };
        };
      };
    };
  };
}
