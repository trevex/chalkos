# The kinds of BGP on Kubernetes images: the node's BGPSpeaker, which names the interfaces whose
# addresses FRR announces, and its BGPPeers, the neighbours FRR runs sessions with.
{ chalkosLib, lib }:
let
  inherit (lib) mkOption types;
  inherit (chalkosLib.internal) builtin cel;
  inherit (chalkosLib.internal.api) groups apiVersions;
  inherit (chalkosLib.types) address secretRef;
  # The documents of a Kubernetes kind in the delivered config, by name.
  delivered = kind: cel.documents "config" apiVersions.kubernetes kind;
  # An interface's name as frr.conf writes it, unquoted: the characters of a Linux interface name,
  # 15 at most, without globs.
  interfaceName = types.strMatching "[A-Za-z0-9_.-]{1,15}";
  # An octet of an IPv4 address in decimal, without leading zeros.
  octet = "25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9]";
  asn = types.ints.between 1 4294967295;
  # The rule that each interface of a list, null for none when nullable, is named by a Network
  # document's [Match] Name=: FRR runs sessions on, and announces the addresses of, interfaces the
  # node configures. A glob names no interface here.
  configured =
    { what, nullable }:
    let
      orNull = lib.optionalString nullable "self == null || ";
    in
    {
      description = "Each interface is named by a Network document's [Match] Name=.";
      rule = ''${orNull}cel.bind(names, ${cel.matchNames "config"}, self.all(i, names.contains(" " + i + " ")))'';
      messageExpression = ''cel.bind(names, ${cel.matchNames "config"}, self.filter(i, !names.contains(" " + i + " "))[0] + " has no Network document whose [Match] Name= names it; ${what}")'';
    };
  # Whether a Node of the delivered config has an IPv4 address in nodeIP.addresses.
  nodeIPv4 = ''cel.bind(nodes, ${delivered "Node"}, nodes.exists(n, has(nodes[n].spec.nodeIP) && has(nodes[n].spec.nodeIP.addresses) && nodes[n].spec.nodeIP.addresses != null && nodes[n].spec.nodeIP.addresses.exists(a, ip(a).family() == "ipv4")))'';
in
{
  BGPSpeaker = builtin groups.kubernetes // {
    description = "The node's side of its BGP sessions: its AS number, its router ID and the interfaces whose addresses FRR announces to every BGPPeer.";
    specRules = [
      {
        description = "A BGPSpeaker gives a router ID unless the Node has an IPv4 address.";
        rule = "self.routerID != null || ${nodeIPv4}";
        message = "the Node's nodeIP.addresses hold no IPv4 address, which FRR would take as its router ID; give routerID, an IPv4 address unique within the node's AS, such as 0.0.0.11";
      }
    ];
    spec = {
      asn = chalkosLib.mkOption {
        type = asn;
        example = 65001;
        description = "AS number of the node. It stays while the node holds a BGPSpeaker; a new one takes a delivery without the BGPSpeaker first.";
        rules = [
          {
            description = "A BGPSpeaker keeps its AS number.";
            rule = "self == oldSelf";
            messageExpression = ''"the AS number stays " + string(oldSelf) + "; to change it, deliver the config without the BGPSpeaker, which ends the node's sessions, and then with the new AS number"'';
          }
        ];
      };
      routerID = chalkosLib.mkOption {
        type = types.nullOr (types.strMatching "(${octet})([.](${octet})){3}");
        default = null;
        example = "0.0.0.11";
        description = "Router ID in the form of an IPv4 address other than 0.0.0.0, unique within the node's AS; null takes the first IPv4 address of the Node's nodeIP.addresses, which a node without one cannot.";
        rules = [
          {
            description = "A router ID is not 0.0.0.0.";
            rule = ''self == null || self != "0.0.0.0"'';
            message = "FRR takes no router ID 0.0.0.0; give the node another one, unique within its AS";
          }
        ];
      };
      export.interfaces = chalkosLib.mkOption {
        type = types.nonEmptyListOf interfaceName;
        example = [ "node0" ];
        unique = {
          across = "list";
          hint = "list each interface once";
        };
        description = "Interfaces whose addresses FRR announces to every BGPPeer, each named by a Network document's [Match] Name=: an address of /32 or /128 as itself, another as its subnet. The VIP is announced while the node holds it on one of them, its Node's vip.interface.";
        rules = [
          (configured {
            what = "FRR announces the addresses of interfaces the node configures";
            nullable = false;
          })
        ];
      };
    };
  };

  BGPPeer = builtin groups.kubernetes // {
    multiple = true;
    description = "A BGP neighbour of the node: its AS number, the address or interfaces of its sessions, their password and BFD, and the routes the node takes from it.";
    specRules = [
      {
        description = "A BGPPeer names an address, interfaces or both.";
        rule = "self.address != null || self.interfaces != null";
        message = "the peer names neither address nor interfaces; give its address, the interfaces of unnumbered sessions, or the address and the one interface its session runs on";
      }
      {
        description = "A BGPPeer with an address names one interface at most.";
        rule = "self.address == null || self.interfaces == null || size(self.interfaces) == 1";
        message = "FRR runs one session per neighbour address, so a peer with an address names one interface at most, the one its session is bound to; give each interface a BGPPeer of its own with the neighbour's address on that link, or leave the address out for unnumbered sessions";
      }
      {
        description = "A node with BGPPeer documents has a BGPSpeaker.";
        rule = "size(${delivered "BGPSpeaker"}) > 0";
        message = "a node with BGPPeer documents needs a BGPSpeaker document, which gives FRR its AS number and router ID";
      }
    ];
    spec = {
      asn = chalkosLib.mkOption {
        type = asn;
        example = 65000;
        description = "AS number of the neighbour.";
        rules = [
          {
            description = "A BGPPeer keeps its AS number.";
            rule = "self == oldSelf";
            messageExpression = ''"the AS number stays " + string(oldSelf) + "; add a BGPPeer of another name for " + string(self)'';
          }
        ];
      };
      address = chalkosLib.mkOption {
        type = types.nullOr address;
        default = null;
        example = "fdc8:a1b2:c3d4:1::1";
        description = "Unicast address of the neighbour, IPv4 or IPv6, neither loopback nor link-local: one session to it, over the route the kernel picks, or bound to the one interface of `interfaces`. Null for unnumbered sessions on `interfaces`. A session to an IPv4 address carries IPv4 routes alone, and one to an IPv6 address without an interface IPv6 routes alone; bound to an interface, a session to an IPv6 address carries both, IPv4 routes with IPv6 next hops.";
        rules = [
          {
            description = "A BGPPeer's address is a unicast address: not unspecified, loopback, link-local or multicast.";
            rule = ''self == null || cel.bind(x, ip(self), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("169.254.0.0/16").contains(x) && !cidr("fe80::/10").contains(x) && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x))'';
            messageExpression = ''"the address " + self + " is no neighbour's: give a unicast address, neither loopback nor link-local; a neighbour on a link without one takes interfaces alone"'';
          }
        ];
      };
      interfaces = chalkosLib.mkOption {
        type = types.nullOr (types.nonEmptyListOf interfaceName);
        default = null;
        example = [
          "fabric0"
          "fabric1"
        ];
        unique = {
          across = "list";
          hint = "list each interface once";
        };
        description = "Interfaces of the sessions, each named by a Network document's [Match] Name=. Without `address`, an unnumbered session on each, to the neighbour's IPv6 link-local address, which carries IPv4 and IPv6 routes; with `address`, the one interface its session is bound to. FRR sends router advertisements on these interfaces, which find the neighbour of an unnumbered session, with a router lifetime of 0, so no host on the link takes the node as its default router.";
        rules = [
          (configured {
            what = "FRR runs sessions on interfaces the node configures";
            nullable = true;
          })
          {
            description = "A BGPPeer's interfaces are not named like IPv4 addresses.";
            rule = ''self == null || self.all(i, !i.matches("^([0-9]{1,3}[.]){3}[0-9]{1,3}$"))'';
            messageExpression = ''"the interface " + self.filter(i, i.matches("^([0-9]{1,3}[.]){3}[0-9]{1,3}$"))[0] + " is named like an address, which FRR cannot tell from a neighbour's; rename the interface"'';
          }
        ];
      };
      password = chalkosLib.mkOption {
        type = types.nullOr secretRef;
        default = null;
        example = {
          secretRef = {
            name = "bgp";
            key = "password";
          };
        };
        description = "Key of a Secret that holds the sessions' TCP MD5 password, of 1 to 80 printable characters without white space; null for none. The Secret's name and key hold letters, digits, `.`, `_` and `-` alone.";
        rules = [
          {
            description = "A BGPPeer's password names its Secret and key as credentials' names.";
            rule = ''self == null || self.secretRef.name.matches("^[A-Za-z0-9._-]+$") && self.secretRef.key.matches("^[A-Za-z0-9._-]+$")'';
            message = "the Secret's name and key hold letters, digits, '.', '_' and '-' alone, as the names of frr.service's credentials do";
          }
        ];
      };
      bfd = mkOption {
        type = types.bool;
        default = true;
        description = "Whether BFD watches each session, so that FRR ends it within about a second of its link failing.";
      };
      import = mkOption {
        type = types.enum [
          "default"
          "none"
        ];
        default = "default";
        description = "Routes the node takes from the neighbour: `default` its default routes, 0.0.0.0/0 and ::/0, alone; `none` nothing. zebra installs a default route from BGP only while the node has no other, such as one from a router advertisement.";
      };
    };
  };
}
