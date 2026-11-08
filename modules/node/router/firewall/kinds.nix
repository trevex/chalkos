# The router's firewall, on images with the router layer: its zones, policies, address sets and
# NAT rules, which the output router-nftables renders (default.nix).
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists. So a rule
# over a policy's firewall rules sits on spec.rules, where document() looks up a rule's AddressSet
# at a constant cost, and lists of lists are flattened before the next loop reads them.
{ lib }:
let
  inherit (lib) mkOption types;
  inherit (import ../../kinds/builtin/lib.nix) builtin groups;
  inherit (lib.chalkos.types) portRange ref;
  inherit (import ../types.nix { inherit lib; })
    interface
    interfaceText
    prefix
    address
    family
    zoneRef
    vrfDocRef
    matchOptions
    matchesFamily
    ofFamily
    oneOfSetAndCIDRs
    ;

  # A FirewallZone document's name, or local for the router itself, which has no document.
  localRef = ref {
    kind = "FirewallZone";
    reserved = [ "local" ];
  };
  verdict = types.enum [
    "accept"
    "drop"
    "reject"
  ];

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
in
{
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
    ];
    spec = {
      # An interface or glob is in one FirewallZone, once, compared as written; the validator
      # router refuses an interface that may be a link of a VRF another zone holds.
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
}
