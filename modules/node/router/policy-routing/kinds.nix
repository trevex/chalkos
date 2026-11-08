# Policy routing on images with the router layer: the PolicyRoute documents' rules, which send
# what they match to a VRF, a table or a next hop, and the NextHop documents, which the outputs
# router-nftables and networkd render (default.nix).
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists. So a rule
# over a policy route's rules sits on spec.rules, where document() looks up a rule's AddressSet or
# NextHop at a constant cost.
{ lib }:
let
  inherit (lib) mkOption types;
  inherit (import ../../kinds/builtin/lib.nix) builtin groups;
  inherit (lib.chalkos.types) ref;
  inherit (import ../types.nix { inherit lib; })
    interface
    interfaceText
    interfaceName
    address
    delivered
    zoneRef
    vrfDocRef
    matchOptions
    matchesFamily
    ofFamily
    oneOfSetAndCIDRs
    ;

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
  # Binds, for body, h to the NextHop documents and tables to their tables.
  nextHopTables =
    body: "cel.bind(h, ${delivered "NextHop"}, cel.bind(tables, h.map(n, h[n].spec.table), ${body}))";
  # Whether the address a is one a next hop may have: none of the unspecified, loopback, multicast
  # and broadcast addresses, which netip's IsGlobalUnicast and IsLinkLocalUnicast leave out, and no
  # IPv4-mapped IPv6 address. A mapped address stands for an IPv4 router, which answers no IPv6
  # neighbour discovery on the link. ip() keeps a mapped address as IPv6, so the IPv4 ranges below
  # would not match it; refusing ::ffff:0:0/96 whole keeps every mapped loopback or multicast out.
  # Link-local addresses are next hops of their link.
  nextHopAddress =
    a:
    ''cel.bind(x, ip(${a}), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x) && x != ip("255.255.255.255") && !cidr("::ffff:0:0/96").contains(x))'';

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
        description = "Routing table that routes the traffic, by number: 254 is the main table. Not 255, the kernel's local table, no VRF's table, which `vrf` names, and no NextHop's table, which `nextHop` names. A destination the table has no route to takes the route it would take without the policy; excludes `vrf` and `nextHop`.";
      };
      nextHop = mkOption {
        type = types.nullOr (ref "NextHop");
        default = null;
        example = "isp-b";
        description = "NextHop document whose router the traffic leaves through, by the default route of the NextHop's table; excludes `vrf` and `table`. Rules that name one NextHop share its table, its route and its mark.";
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
  PolicyRoute = builtin groups.router // {
    multiple = true;
    description = "Policy routing of the router: rules that send the forwarded traffic they match, coming in on a zone's links, on interfaces or on any link, to a VRF's routing table, another table or a next hop, whatever its destination's route. The first rule that matches decides; documents apply in the order of their names.";
    rules = [
      {
        description = "A PolicyRoute's in names a zone or interfaces, one of them.";
        rule = ''documents.all(n, [dyn(documents[n].spec["in"])].all(i, i == null || (i.zone == null) != (size(i.interfaces) == 0)))'';
        messageExpression = ''documents.filter(n, [dyn(documents[n].spec["in"])].exists(i, i != null && (i.zone == null) == (size(i.interfaces) == 0))).map(n, "PolicyRoute " + n).join(", ") + ": in names a zone or interfaces, one of them; leave in out for traffic from every link"'';
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
          # The reference check has found the NextHop, so document() returns it.
          (policyRulesRule {
            description = "A rule with a next hop matches packets of the NextHop's family.";
            ok = ''p.to.nextHop == null || (ip(document("NextHop", p.to.nextHop).spec.address).family() == "ipv4" ? ${matchesFamily "ipv4"} : ${matchesFamily "ipv6"})'';
            message = "a rule's next hop is of another family than the packets the rule matches";
          })
          {
            description = "A rule's table is not 255, the kernel's local table, and no VRF's table.";
            rule = vrfTables "self.all(p, p.to.table == null || p.to.table != 255 && !(p.to.table in tables))";
            message = "a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table";
          }
          {
            description = "A rule's table is no NextHop's table, which holds the NextHop's routes alone.";
            rule = nextHopTables "self.all(p, p.to.table == null || !(p.to.table in tables))";
            messageExpression = nextHopTables ''cel.bind(t, self.filter(p, p.to.table != null && p.to.table in tables)[0].to.table, "a rule's table " + string(t) + " is the table of NextHop " + h.filter(n, h[n].spec.table == t)[0] + "; name the NextHop in nextHop, or take another table")'';
          }
        ];
      };
    };
  };

  NextHop = builtin groups.router // {
    multiple = true;
    description = "A next hop of policy routing: the router at an address on one of the node's links, and a routing table of its own that holds a default route through it and an unreachable default route after it, so that the traffic is dropped while the link is down. PolicyRoute rules name it in `to.nextHop`; the rules that name one NextHop share its table, its route and its mark.";
    spec = {
      address = lib.chalkos.mkOption {
        type = address;
        example = "198.51.100.1";
        description = "Address of the router the traffic leaves through: a unicast address, link-local ones included, and no IPv4-mapped IPv6 address. The rules that name the NextHop match packets of its family.";
        rules = [
          {
            description = "A NextHop's address is a unicast address: not unspecified, loopback, multicast, broadcast or IPv4-mapped IPv6.";
            rule = nextHopAddress "self";
            messageExpression = ''"the next hop " + self + " is no unicast address of a router, or is an IPv4-mapped IPv6 address; give the router's address, an IPv4 one as IPv4"'';
          }
        ];
      };
      interface = lib.chalkos.mkOption {
        type = interfaceName;
        example = "wanb0";
        description = "Link the next hop is on, which a Network document names alone in its `[Match]` section; the route through the next hop goes into that Network.";
        rules = [
          {
            description = "A NextHop's interface is not lo.";
            rule = ''self != "lo"'';
            message = "the interface is lo, which no next hop is on; name the link the next hop is on";
          }
        ];
      };
      table = lib.chalkos.mkOption {
        type = types.ints.between 1 4294967295;
        unique.hint = "give each NextHop a table of its own";
        example = 200;
        description = "Routing table of the next hop, which no other NextHop uses: none of 253, 254 and 255, the kernel's own, no VRF's table and no PolicyRoute rule's `table`.";
        rules = [
          {
            description = "A NextHop's table is none of 253, 254 and 255, the kernel's own.";
            rule = "!(self in [253, 254, 255])";
            message = "the table is 253, 254 or 255, the kernel's own; give the NextHop a table of its own";
          }
          {
            description = "A NextHop's table is no VRF's table.";
            rule = vrfTables "!(self in tables)";
            messageExpression = ''cel.bind(v, ${delivered "VRF"}, "the table is that of VRF " + v.filter(n, v[n].spec.table == self)[0] + "; give the NextHop a table of its own")'';
          }
        ];
      };
    };
  };
}
