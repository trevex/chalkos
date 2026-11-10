# BGP on router images whose chalkos.router.bgp.enable is true: a BGP instance per VRF, its peers,
# and the prefix lists, community lists and routing policies peers, networks and imports name,
# which the output frr renders into FRR's config (default.nix).
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists. So the
# rules over a peer's families sit on spec.families and those over an instance's networks and
# imports on spec.networks and spec.import, where the one document is self, and loop over a
# policy's terms alone. What depends on the links networkd puts in each VRF or on more than one
# peer the validator router checks: a peer's interface of another VRF or of a VRF's device, a
# session two peers name and listen ranges of a VRF that overlap. What depends on FRR's config the
# output frr checks: a policy two AS numbers prepend and an AS path bgpd refuses.
{ lib }:
let
  inherit (lib) mkOption types;
  inherit (lib.chalkos.internal) builtin;
  inherit (lib.chalkos.internal.api) groups;
  inherit (lib.chalkos.internal.kindTypes) peerAS;
  inherit (lib.chalkos.types) ref secretRef;
  inherit (import ../types.nix { inherit lib; })
    interface
    interfaceText
    interfaceName
    prefix
    address
    family
    delivered
    vrfRef
    ;

  policyRef = ref "RoutingPolicy";
  # A number from 1 to 2^32 - 1, as AS numbers and BGP's counts are.
  positive32 = types.ints.between 1 4294967295;
  # Numbers of 16 and of 32 bits in decimal without leading zeros, as a community's parts are: a
  # community has one spelling, so two compare equal as written when FRR takes them for one.
  uint16 = "[0-9]|[1-9][0-9]{1,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5]";
  uint32 = "[0-9]|[1-9][0-9]{1,8}|[1-3][0-9]{9}|4[01][0-9]{8}|42[0-8][0-9]{7}|429[0-3][0-9]{6}|4294[0-8][0-9]{5}|42949[0-5][0-9]{4}|429496[0-6][0-9]{3}|4294967[01][0-9]{2}|42949672[0-8][0-9]|429496729[0-5]";
  # A standard community, <AS>:<value>, or a large one, <AS>:<value>:<value>.
  community = types.strMatching "(${uint16}):(${uint16})|(${uint32}):(${uint32}):(${uint32})";
  communityText = "standard communities `<AS>:<value>` of 16-bit numbers, or large ones `<AS>:<value>:<value>` of 32-bit numbers, without leading zeros";
  # An octet of an IPv4 address in decimal, without leading zeros.
  octet = "25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9]";
  # Whether the address a is one a peer's address may be: a unicast address beyond the router. It
  # is none of the unspecified, loopback, multicast and broadcast addresses and no link-local
  # address, as a neighbour on a link takes interface or interfaces. It is no IPv4-mapped IPv6
  # address either, which stands for an IPv4 neighbour that a session reaches at its IPv4
  # address; refusing the mapped range whole keeps out a mapped loopback or multicast address,
  # which the IPv4 ranges below do not match.
  neighbourAddress =
    a:
    ''cel.bind(x, ip(${a}), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("169.254.0.0/16").contains(x) && !cidr("fe80::/10").contains(x) && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x) && x != ip("255.255.255.255") && !cidr("::ffff:0:0/96").contains(x))'';
  # A name of an interface that reads as an address: four numbers with dots, or hex digits and dots
  # with two colons at least.
  addressLike = "^([0-9]{1,3}[.]){3}[0-9]{1,3}$|^[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*$";
  # The rule that refuses an interface's name that reads as an address, for the field of
  # interfaces given.
  notAddressLike = interfaces: {
    description = "A BGPPeer's interfaces are not named like addresses.";
    rule = ''self == null || ${interfaces}.all(i, !i.matches("${addressLike}"))'';
    messageExpression = ''"the interface " + ${interfaces}.filter(i, i.matches("${addressLike}"))[0] + " is named like an address, which FRR cannot tell from a neighbour's; give the peer its address, or rename the interface"'';
  };

  # The vrf of the running BGP document of the name n, default for the default VRF; a running
  # config of an image whose BGP had no vrf lacks the field.
  oldVRF = n: ''(has(oldDocuments[${n}].spec.vrf) ? oldDocuments[${n}].spec.vrf : "default")'';
  # Whether the BGP spec s runs in the default VRF or announces no default route. Every other
  # VRF's table ends in an unreachable default route, which a network of length 0 would match at
  # all times.
  noDefaultNetwork =
    s: ''(${s}.vrf == "default" || ${s}.networks.all(w, cidr(w.prefix).prefix() > 0))'';
  # The VRFs the config's BGP documents serve.
  served = "cel.bind(bgps, ${delivered "BGP"}, bgps.map(n, bgps[n].spec.vrf))";
  # Whether the BGPPeer spec s carries its IPv4 routes with next hops its session can: a peer at an
  # IPv6 address or with an IPv6 listen range that exchanges IPv4 routes sends them with IPv6 next
  # hops, which the session carries with extended next hops alone. FRR negotiates them on a link's
  # session by itself.
  nextHopFits =
    s:
    ''(${s}.families.ipv4 == null || ${s}.extendedNextHop || (${s}.address == null || ip(${s}.address).family() == "ipv4") && (${s}.listen == null || cidr(${s}.listen.range).ip(0).family() == "ipv4"))'';
  # What a BGP instance imports of one family from other VRFs' instances.
  leak = types.submodule {
    options = {
      vrfs = lib.chalkos.mkOption {
        type = types.nonEmptyListOf vrfRef;
        unique = {
          across = "list";
          hint = "name each VRF once";
        };
        example = [ "tenant" ];
        description = "VRFs whose BGP instances' routes of the family the instance takes, each once, by the names of their VRF documents or `default`.";
      };
      policy = mkOption {
        type = types.nullOr policyRef;
        default = null;
        description = "RoutingPolicy that decides the routes the instance takes from all of `vrfs`; null takes every route.";
      };
    };
  };
  # Whether the RoutingPolicy p matches PrefixLists of the family f: the first it names is of f, as
  # the policy's other lists are by a rule of its own. The reference check has found the policy
  # and its lists, so document() returns them.
  policyOfFamily =
    p: f:
    ''cel.bind(named, document("RoutingPolicy", ${p}).spec.terms.filter(t, t.match.prefixList != null), size(named) == 0 || document("PrefixList", named[0].match.prefixList).spec.family == ${f})'';
  # Whether the RoutingPolicy of each import and export of a peer's families, self, passes ok, a
  # function of the policy and the family. Written out per family and direction, as a loop over
  # them would cost a loop more.
  eachFamilyPolicy =
    ok:
    lib.concatMapStringsSep " && "
      (
        f:
        lib.concatMapStringsSep " && "
          (d: ''(self.${f} == null || self.${f}["${d}"] == null || ${ok ''self.${f}["${d}"]'' ''"${f}"''})'')
          [
            "import"
            "export"
          ]
      )
      [
        "ipv4"
        "ipv6"
      ];
  # The first import or export of a peer's families, self, whose policy fails ok, as [policy,
  # family], for a rule's message.
  firstFamilyPolicy =
    ok:
    "[${
      lib.concatMapStringsSep ", "
        (
          f:
          lib.concatMapStringsSep ", "
            (
              d:
              ''(self.${f} == null || self.${f}["${d}"] == null || ${ok ''self.${f}["${d}"]'' ''"${f}"''} ? [] : [self.${f}["${d}"], "${f}"])''
            )
            [
              "import"
              "export"
            ]
        )
        [
          "ipv4"
          "ipv6"
        ]
    }].filter(u, size(u) > 0)[0]";
  # Whether the RoutingPolicy of the network w matches PrefixLists of the network's family.
  networkPolicy = w: policyOfFamily "${w}.policy" "cidr(${w}.prefix).ip(0).family()";
  # Whether the defaultOriginateWhen of each of a peer's families, self, that gives one passes ok,
  # a function of the family's field and the family's name as a CEL string. Written out per
  # family, as a loop over them would cost a loop more.
  eachTracked =
    ok:
    lib.concatMapStringsSep " && "
      (f: "(self.${f} == null || self.${f}.defaultOriginateWhen == null || ${ok "self.${f}" ''"${f}"''})")
      [
        "ipv4"
        "ipv6"
      ];
  # The first family of a peer, self, whose defaultOriginateWhen fails ok, as [family, list], for
  # a rule's message.
  firstTracked =
    ok:
    "[${
      lib.concatMapStringsSep ", "
        (
          f:
          ''(self.${f} == null || self.${f}.defaultOriginateWhen == null || ${ok "self.${f}" ''"${f}"''} ? [] : ["${f}", self.${f}.defaultOriginateWhen])''
        )
        [
          "ipv4"
          "ipv6"
        ]
    }].filter(u, size(u) > 0)[0]";
  # Whether the family f's list is a PrefixList of the family n.
  trackedOfFamily = f: n: ''document("PrefixList", ${f}.defaultOriginateWhen).spec.family == ${n}'';

  # What a peer does with the routes of one address family.
  peerFamily = types.submodule {
    options = {
      import = mkOption {
        type = types.nullOr policyRef;
        default = null;
        description = "RoutingPolicy that decides the routes the router accepts from the peer; null accepts every route.";
      };
      export = mkOption {
        type = types.nullOr policyRef;
        default = null;
        description = "RoutingPolicy that decides the routes the router sends the peer; null sends every route the router has.";
      };
      defaultOriginate = mkOption {
        type = types.bool;
        default = false;
        description = "Whether the router sends the peer a default route of the family: whatever its own table holds, or while `defaultOriginateWhen` holds.";
      };
      defaultOriginateWhen = mkOption {
        type = types.nullOr (ref "PrefixList");
        default = null;
        example = "uplink";
        description = "PrefixList of the family that decides when the router sends the default route of `defaultOriginate`: while the peer's VRF holds a route the list matches, from BGP, from the kernel, such as a route from DHCP, from router advertisements or a `Network`'s `[Route]`, or a link's prefix. Blackhole, unreachable and prohibit routes do not count. The router does not announce the routes it tracks. null sends the default route whatever the table holds.";
      };
      maxPrefix = mkOption {
        type = types.nullOr positive32;
        default = null;
        description = "Routes the router accepts from the peer at most; past them it ends the session. null sets no limit.";
      };
    };
  };
in
{
  BGP = builtin groups.router // {
    multiple = true;
    description = "A BGP instance of the router in one VRF: its AS number and router ID, how many equal paths it installs, its BFD timers, the networks it announces and the routes it imports from other VRFs.";
    rules = [
      {
        description = "A BGP instance does not import its own VRF.";
        rule = ''documents.all(n, cel.bind(s, documents[n].spec, [s["import"].ipv4, s["import"].ipv6].all(f, f == null || !(documents[n].spec.vrf in f.vrfs))))'';
        messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, ![s["import"].ipv4, s["import"].ipv6].all(f, f == null || !(documents[n].spec.vrf in f.vrfs)))).map(n, "BGP " + n).join(", ") + " imports the routes of its own VRF; name other VRFs"'';
      }
      {
        description = "A delivery that drops the default VRF's BGP document keeps no running BGP instance of another VRF, as FRR keeps its default instance while another runs.";
        rule = ''!oldDocuments.exists(n, ${oldVRF "n"} == "default") || documents.exists(n, documents[n].spec.vrf == "default") || !documents.exists(n, documents[n].spec.vrf != "default" && oldDocuments.exists(m, ${oldVRF "m"} == documents[n].spec.vrf))'';
        message = "the config drops the default VRF's BGP document and keeps a running BGP instance of another VRF, which FRR does not allow; drop the other VRFs' BGP documents in the same delivery, or keep the default VRF's";
      }
      {
        description = "A BGP instance of a VRF other than the default one announces no default route among its networks.";
        rule = "documents.all(n, ${noDefaultNetwork "documents[n].spec"})";
        messageExpression = ''documents.filter(n, !${noDefaultNetwork "documents[n].spec"}).map(n, "BGP " + n + " announces the network " + documents[n].spec.networks.filter(w, cidr(w.prefix).prefix() == 0)[0].prefix + " in VRF " + documents[n].spec.vrf).join(", ") + ", which would always be announced, as a VRF's table ends in an unreachable default route; use a peer family's defaultOriginate"'';
      }
    ];
    spec = {
      vrf = lib.chalkos.mkOption {
        type = vrfRef;
        unique.hint = "a VRF has one BGP instance";
        default = "default";
        example = "tenant";
        description = "VRF document whose instance this is; `default` for the default VRF, which holds every link no VRF holds.";
      };
      asn = lib.chalkos.mkOption {
        type = positive32;
        example = 64512;
        description = "AS number of the instance. It stays while the BGP document keeps its name; a BGP document of another name replaces the old one, AS number included.";
        rules = [
          {
            description = "BGP keeps its AS number.";
            rule = "self == oldSelf";
            messageExpression = ''"the AS number stays " + string(oldSelf) + "; to change it, deliver the config without the BGP document, which ends the router's sessions, and then with the new AS number"'';
          }
        ];
      };
      routerID = lib.chalkos.mkOption {
        type = types.strMatching "(${octet})([.](${octet})){3}";
        example = "192.0.2.1";
        description = "Router ID, an IPv4 address other than 0.0.0.0, a multicast address and 255.255.255.255, which the router's peers know it by; give an IPv6-only router one too.";
        rules = [
          {
            description = "A router ID is an IPv4 unicast address: not 0.0.0.0, multicast or 255.255.255.255.";
            rule = ''cel.bind(a, ip(self), a != ip("0.0.0.0") && !cidr("224.0.0.0/4").contains(a) && a != ip("255.255.255.255"))'';
            messageExpression = ''"the router ID " + self + " is no IPv4 unicast address; give one that is neither 0.0.0.0, multicast nor 255.255.255.255"'';
          }
        ];
      };
      multipath = {
        ebgp = mkOption {
          type = types.ints.between 1 64;
          default = 1;
          description = "Equal paths from external peers the router installs for one prefix at most.";
        };
        ibgp = mkOption {
          type = types.ints.between 1 64;
          default = 1;
          description = "Equal paths from internal peers the router installs for one prefix at most.";
        };
        asPathRelax = mkOption {
          type = types.bool;
          default = false;
          description = "Whether paths through different neighbouring ASes count as equal when their AS paths are equally long, as for two leaves of a fabric.";
        };
      };
      bfd = {
        receiveMs = mkOption {
          type = types.ints.between 10 60000;
          default = 300;
          description = "Shortest interval in milliseconds at which the router accepts BFD packets of the peers with `bfd`.";
        };
        transmitMs = mkOption {
          type = types.ints.between 10 60000;
          default = 300;
          description = "Shortest interval in milliseconds at which the router sends BFD packets.";
        };
        multiplier = mkOption {
          type = types.ints.between 2 255;
          default = 3;
          description = "BFD packets in a row a peer may miss before the router takes it for down and ends the session.";
        };
      };
      gracefulRestart = mkOption {
        type = types.bool;
        default = false;
        description = "Whether the router keeps a peer's routes while the peer restarts and asks its peers to keep its own.";
      };
      networks = lib.chalkos.mkOption {
        type = types.listOf (
          types.submodule {
            options = {
              prefix = mkOption {
                type = prefix;
                example = "2001:db8:ffff::/48";
                description = "Address range the router announces while its table holds a route to it.";
              };
              policy = mkOption {
                type = types.nullOr policyRef;
                default = null;
                description = "RoutingPolicy whose terms set the announcement's attributes; null announces it as it is.";
              };
            };
          }
        );
        default = [ ];
        description = "Networks the router announces to its peers.";
        unique = {
          by = [
            {
              field = "prefix";
              as = "cidr";
            }
          ];
          hint = "give each range one network";
        };
        rules = [
          {
            description = "A network's policy matches PrefixLists of the network's family alone.";
            rule = "self.all(w, w.policy == null || ${networkPolicy "w"})";
            messageExpression = ''cel.bind(w, self.filter(w, w.policy != null && !${networkPolicy "w"})[0], "the RoutingPolicy " + w.policy + " of the network " + w.prefix + " matches PrefixLists of another family")'';
          }
        ];
      };
      import = lib.chalkos.mkOption {
        type = types.submodule {
          options = {
            ipv4 = mkOption {
              type = types.nullOr leak;
              default = null;
              description = "IPv4 routes the instance takes from other VRFs' instances; null takes none.";
            };
            ipv6 = mkOption {
              type = types.nullOr leak;
              default = null;
              description = "IPv6 routes the instance takes from other VRFs' instances; null takes none.";
            };
          };
        };
        default = { };
        description = "Routes the instance takes from the BGP instances of other VRFs, which the kernel installs in this VRF's table with their next hops in the other VRF. Static routes of a VRF's links are not leaked.";
        rules = [
          {
            description = "An import's VRFs have BGP documents.";
            rule = "cel.bind(have, ${served}, [self.ipv4, self.ipv6].all(f, f == null || f.vrfs.all(v, v in have)))";
            messageExpression = ''cel.bind(have, ${served}, "the import names the VRF " + [self.ipv4, self.ipv6].filter(f, f != null).map(f, f.vrfs.filter(v, !(v in have))).flatten()[0] + ", which no BGP document serves; leaking takes routes from BGP alone")'';
          }
          {
            description = "An import's policy matches PrefixLists of its family alone.";
            rule = "(self.ipv4 == null || self.ipv4.policy == null || ${policyOfFamily "self.ipv4.policy" ''"ipv4"''}) && (self.ipv6 == null || self.ipv6.policy == null || ${policyOfFamily "self.ipv6.policy" ''"ipv6"''})";
            messageExpression = ''"the " + (self.ipv4 == null || self.ipv4.policy == null || ${policyOfFamily "self.ipv4.policy" ''"ipv4"''} ? "ipv6" : "ipv4") + " import's RoutingPolicy matches PrefixLists of another family"'';
          }
        ];
      };
    };
  };

  BGPPeer = builtin groups.router // {
    multiple = true;
    description = "A BGP neighbour of the router, or the neighbours on a set of interfaces or of a prefix: where to reach them, their AS, and what the router accepts from them and sends them in each address family.";
    rules = [
      {
        description = "A BGPPeer names exactly one of address, interface, interfaces and listen.";
        rule = "documents.all(n, cel.bind(s, documents[n].spec, [s.address, s.interface, s.interfaces, s.listen].filter(x, x != null).size() == 1))";
        messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, [s.address, s.interface, s.interfaces, s.listen].filter(x, x != null).size() != 1)).map(n, "BGPPeer " + n).join(", ") + " names not exactly one of address, interface, interfaces and listen; a peer is reached at an address, on an interface or on each interface of a list, or accepted from a listen range"'';
      }
      {
        description = "A BGPPeer's VRF has a BGP document.";
        rule = "cel.bind(have, ${served}, documents.all(n, documents[n].spec.vrf in have))";
        messageExpression = ''cel.bind(have, ${served}, documents.filter(n, !(documents[n].spec.vrf in have)).map(n, "BGPPeer " + n + " runs in the VRF " + documents[n].spec.vrf).join(", ") + ": a peer's VRF needs a BGP document, which gives the router its AS number there")'';
      }
      {
        description = "A BGPPeer at an IPv6 address or with an IPv6 listen range that exchanges IPv4 routes has extendedNextHop.";
        rule = "documents.all(n, ${nextHopFits "documents[n].spec"})";
        messageExpression = ''documents.filter(n, !${nextHopFits "documents[n].spec"}).map(n, "BGPPeer " + n + " exchanges IPv4 routes " + (documents[n].spec.address != null ? "at the IPv6 address " + documents[n].spec.address : "from the IPv6 listen range " + documents[n].spec.listen.range)).join(", ") + " without extendedNextHop: true; the routes' next hops are IPv6 addresses, which a session carries with extendedNextHop alone"'';
      }
    ];
    spec = {
      vrf = mkOption {
        type = vrfRef;
        default = "default";
        example = "tenant";
        description = "VRF document whose BGP instance runs the session; `default` for the default VRF. The peer's interfaces are links of that VRF.";
      };
      address = lib.chalkos.mkOption {
        type = types.nullOr address;
        default = null;
        example = "2001:db8:ffff::2";
        description = "Unicast address of the neighbour beyond the router, neither loopback, link-local nor IPv4-mapped; excludes `interface`, `interfaces` and `listen`.";
        rules = [
          {
            description = "A BGPPeer's address is a unicast address beyond the router: not unspecified, loopback, link-local, multicast, broadcast or IPv4-mapped IPv6.";
            rule = "self == null || ${neighbourAddress "self"}";
            messageExpression = ''"the address " + self + " is no neighbour's: give a unicast address beyond the router, neither loopback, link-local nor IPv4-mapped; a neighbour on a link takes interface or interfaces"'';
          }
        ];
      };
      interface = lib.chalkos.mkOption {
        type = types.nullOr interfaceName;
        default = null;
        example = "eth1";
        description = "Interface on whose link the router finds the neighbour by its IPv6 link-local address, without an address of its own on the link; excludes `address`, `interfaces` and `listen`. A name that reads as an address is refused.";
        rules = [ (notAddressLike "[self]") ];
      };
      interfaces = lib.chalkos.mkOption {
        type = types.nullOr (types.nonEmptyListOf interface);
        default = null;
        example = [
          "eth1"
          "eth2"
        ];
        description = "Interfaces, ${interfaceText}, with a session each, as for `interface`; a glob stands for the interfaces of the peer's VRF that the node's Network documents name in a `[Match]` section's `Name=`. Excludes `address`, `interface` and `listen`.";
        rules = [ (notAddressLike "self") ];
      };
      listen = lib.chalkos.mkOption {
        type = types.nullOr (
          types.submodule {
            options.range = mkOption {
              type = prefix;
              example = "2001:db8:0:1::/64";
              description = "Prefix whose addresses may open a session with the router; neither link-local, loopback, multicast nor IPv4-mapped.";
            };
          }
        );
        default = null;
        example = {
          range = "2001:db8:0:1::/64";
        };
        description = "Accept a session from every neighbour whose address lies in `range`, which then shares the peer's settings; the router opens no session itself. Excludes `address`, `interface` and `interfaces`.";
        # The first rule checks the range's first address, as FRR checks a range for a link-local
        # one; the second refuses a range of length 0, whose first address the first would
        # refuse with a message that misleads.
        rules = [
          {
            description = "A BGPPeer's listen range starts at an address a neighbour may have: not in link-local, loopback, multicast or IPv4-mapped IPv6 space.";
            rule = "self == null || cidr(self.range).prefix() == 0 || ${neighbourAddress "string(cidr(self.range).ip(0))"}";
            messageExpression = ''"the listen range " + self.range + " starts in link-local, loopback, multicast or IPv4-mapped space, where no neighbour's address lies; give the neighbours' prefix. FRR accepts no link-local range"'';
          }
          {
            description = "A BGPPeer's listen range is longer than 0 bits.";
            rule = "self == null || cidr(self.range).prefix() > 0";
            messageExpression = ''"a listen range of length 0, " + self.range + ", accepts a session from every address; give the neighbours' prefix"'';
          }
        ];
      };
      remoteAS = mkOption {
        type = peerAS;
        example = "external";
        description = "AS of the neighbour: a number, `external` for any AS but the router's, or `internal` for the router's own.";
      };
      password = lib.chalkos.mkOption {
        type = types.nullOr secretRef;
        default = null;
        example = {
          secretRef = {
            name = "bgp";
            key = "fabric";
          };
        };
        description = "Key of a Secret that holds the session's TCP MD5 password, of 80 characters at most and no white space; null for none. The Secret's name and key hold letters, digits, `.`, `_` and `-` alone.";
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
        default = false;
        description = "Whether the router checks the session's link with BFD, at the timers of the BGP document, and ends the session when BFD finds it down.";
      };
      extendedNextHop = mkOption {
        type = types.bool;
        default = false;
        description = "Whether the router sends and accepts IPv4 routes with IPv6 next hops (RFC 8950). A peer at an IPv6 address or with an IPv6 listen range that exchanges IPv4 routes needs it. FRR negotiates it on sessions of `interface` and `interfaces` whatever this says.";
      };
      families = lib.chalkos.mkOption {
        type = types.submodule {
          options = {
            ipv4 = mkOption {
              type = types.nullOr peerFamily;
              default = null;
              description = "IPv4 routes of the session; null exchanges none.";
            };
            ipv6 = mkOption {
              type = types.nullOr peerFamily;
              default = null;
              description = "IPv6 routes of the session; null exchanges none.";
            };
          };
        };
        default = { };
        description = "The address families the session exchanges routes of, and how.";
        rules = [
          {
            description = "A BGPPeer exchanges the routes of one family at least.";
            rule = "self.ipv4 != null || self.ipv6 != null";
            message = "the peer exchanges the routes of no family; give families.ipv4 or families.ipv6, an empty one accepting and sending every route";
          }
          {
            description = "A family's policies match PrefixLists of that family alone.";
            rule = eachFamilyPolicy policyOfFamily;
            messageExpression = ''cel.bind(u, ${firstFamilyPolicy policyOfFamily}, "the " + u[1] + " family's RoutingPolicy " + u[0] + " matches a PrefixList of another family")'';
          }
          {
            description = "A family's defaultOriginateWhen comes with defaultOriginate.";
            rule = eachTracked (f: _: "${f}.defaultOriginate");
            messageExpression = ''cel.bind(u, ${
              firstTracked (f: _: "${f}.defaultOriginate")
            }, "the " + u[0] + " family names defaultOriginateWhen without defaultOriginate: true; defaultOriginateWhen says when the router sends the default route that defaultOriginate originates")'';
          }
          {
            description = "A family's defaultOriginateWhen is a PrefixList of that family.";
            rule = eachTracked trackedOfFamily;
            messageExpression = ''cel.bind(u, ${firstTracked trackedOfFamily}, "the " + u[0] + " family's defaultOriginateWhen names the PrefixList " + u[1] + " of " + document("PrefixList", u[1]).spec.family + "; it tracks routes of the family whose default route it sends")'';
          }
        ];
      };
      description = lib.chalkos.mkOption {
        type = types.nullOr (types.strMatching "[!-?A-~]+( [!-?A-~]+)*");
        default = null;
        description = "What the neighbour is, in up to 80 printable ASCII characters other than `@`, with a single space between two words.";
        rules = [
          {
            description = "A BGPPeer's description holds 80 characters at most.";
            rule = "self == null || size(self) <= 80";
            message = "the description holds more than 80 characters";
          }
        ];
      };
      shutdown = mkOption {
        type = types.bool;
        default = false;
        description = "Whether the router keeps the session down while the document stays.";
      };
    };
  };

  PrefixList = builtin groups.router // {
    multiple = true;
    description = "Address ranges of one family that a RoutingPolicy term matches routes against: entries in order, the first that matches a route deciding.";
    rules = [
      {
        description = "A PrefixList's entries are of its family, with ge and le from the prefix length to the family's longest prefix, ge no more than le.";
        rule = ''documents.all(n, cel.bind(s, documents[n].spec, s.entries.all(e, cel.bind(c, cidr(e.prefix), cel.bind(max, s.family == "ipv4" ? 32 : 128, c.ip(0).family() == s.family && (e.ge == null || e.ge >= c.prefix() && e.ge <= max) && (e.le == null || e.le >= c.prefix() && e.le <= max) && (e.ge == null || e.le == null || e.ge <= e.le))))))'';
        messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, !s.entries.all(e, cel.bind(c, cidr(e.prefix), cel.bind(max, s.family == "ipv4" ? 32 : 128, c.ip(0).family() == s.family && (e.ge == null || e.ge >= c.prefix() && e.ge <= max) && (e.le == null || e.le >= c.prefix() && e.le <= max) && (e.ge == null || e.le == null || e.ge <= e.le)))))).map(n, "PrefixList " + n).join(", ") + " has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"'';
      }
    ];
    spec = {
      family = mkOption {
        type = family;
        description = "Address family of the list.";
      };
      entries = lib.chalkos.mkOption {
        type = types.nonEmptyListOf (
          types.submodule {
            options = {
              prefix = mkOption {
                type = prefix;
                example = "2001:db8::/32";
                description = "Address range the entry matches.";
              };
              ge = mkOption {
                type = types.nullOr (types.ints.between 0 128);
                default = null;
                description = "Shortest prefix length of a route within `prefix` that the entry matches, from the prefix's length; null matches `prefix` alone, unless `le` is given.";
              };
              le = mkOption {
                type = types.nullOr (types.ints.between 0 128);
                default = null;
                description = "Longest prefix length of a route within `prefix` that the entry matches; null matches `prefix` alone, unless `ge` is given.";
              };
              action = mkOption {
                type = types.enum [
                  "permit"
                  "deny"
                ];
                default = "permit";
                description = "Whether a route the entry matches matches the list.";
              };
            };
          }
        );
        description = "Entries in order; a route no entry matches does not match the list.";
        # No two entries have one prefix, ge and le, as the later would never decide; a null
        # ge or le equals another's null.
        unique = {
          by = [
            {
              field = "prefix";
              as = "cidr";
            }
            "ge"
            "le"
          ];
          hint = "the later never decides; drop one";
        };
      };
    };
  };

  CommunityList = builtin groups.router // {
    multiple = true;
    description = "BGP communities that a RoutingPolicy term matches routes against: a route that carries one of them matches.";
    rules = [
      {
        description = "A CommunityList holds standard or large communities, not both.";
        rule = ''documents.all(n, cel.bind(c, documents[n].spec.communities, cel.bind(large, c[0].indexOf(":") != c[0].lastIndexOf(":"), c.all(x, (x.indexOf(":") != x.lastIndexOf(":")) == large))))'';
        messageExpression = ''documents.filter(n, cel.bind(c, documents[n].spec.communities, cel.bind(large, c[0].indexOf(":") != c[0].lastIndexOf(":"), !c.all(x, (x.indexOf(":") != x.lastIndexOf(":")) == large)))).map(n, "CommunityList " + n).join(", ") + " holds standard and large communities; give each kind a list of its own"'';
      }
    ];
    # The pattern takes each community in one spelling, so a repeat compares as written.
    spec.communities = lib.chalkos.mkOption {
      type = types.nonEmptyListOf community;
      unique = {
        across = "list";
        hint = "name each community once";
      };
      example = [ "64512:100" ];
      description = "The communities: ${communityText}.";
    };
  };

  RoutingPolicy = builtin groups.router // {
    multiple = true;
    description = "What the router does with the routes of a peer's family or of a network: terms in order, the first that matches a route deciding, then a default.";
    spec = {
      terms = lib.chalkos.mkOption {
        type = types.listOf (
          types.submodule {
            options = {
              match = {
                prefixList = mkOption {
                  type = types.nullOr (ref "PrefixList");
                  default = null;
                  description = "PrefixList the route's prefix matches; null matches every prefix.";
                };
                communityList = mkOption {
                  type = types.nullOr (ref "CommunityList");
                  default = null;
                  description = "CommunityList one of whose communities the route carries; null matches every route.";
                };
                asPath = mkOption {
                  type = types.nullOr (types.strMatching "[0-9_^$.*+?()|{},-]+( [0-9_^$.*+?()|{},-]+)*");
                  default = null;
                  example = "^64513_";
                  description = "Regular expression the route's AS path matches, with `_` for the start, the end or a space between two ASes; null matches every path. It holds up to 200 digits, single spaces and `_^$.*+?()|{},-`. The schema and rules check these characters alone; the output frr refuses an expression that bgpd cannot compile.";
                };
              };
              "then" = {
                action = mkOption {
                  type = types.enum [
                    "accept"
                    "reject"
                  ];
                  description = "Whether the router takes a route the term matches.";
                };
                localPref = mkOption {
                  type = types.nullOr (types.ints.between 0 4294967295);
                  default = null;
                  description = "Local preference the route gets; null keeps it.";
                };
                med = mkOption {
                  type = types.nullOr (types.ints.between 0 4294967295);
                  default = null;
                  description = "Multi-exit discriminator the route gets; null keeps it.";
                };
                addCommunities = mkOption {
                  type = types.listOf community;
                  default = [ ];
                  description = "Communities added to the route: ${communityText}.";
                };
                removeCommunities = mkOption {
                  type = types.listOf community;
                  default = [ ];
                  description = "Communities removed from the route.";
                };
                prepend = mkOption {
                  type = types.nullOr (types.ints.between 1 10);
                  default = null;
                  description = "How often the router puts its own AS number in front of the route's AS path; null puts none.";
                };
              };
            };
          }
        );
        default = [ ];
        description = "Terms in order: the first whose match holds for a route decides.";
        rules = [
          {
            description = "A RoutingPolicy holds 6000 terms at most.";
            rule = "size(self) <= 6000";
            messageExpression = ''"the policy holds " + string(size(self)) + " terms; a route-map holds 6000 terms at most, numbered 10, 20 and on below its default's 65535"'';
          }
          {
            description = "A term's asPath holds 200 characters at most.";
            rule = "self.all(t, t.match.asPath == null || size(t.match.asPath) <= 200)";
            message = "a term's asPath holds more than 200 characters";
          }
          {
            description = "A RoutingPolicy's PrefixLists are of one family.";
            rule = ''cel.bind(families, self.filter(t, t.match.prefixList != null).map(t, document("PrefixList", t.match.prefixList).spec.family), families.all(f, f == families[0]))'';
            message = "the terms match PrefixLists of both families; a policy serves the routes of one family, so give each family a policy of its own";
          }
          {
            description = "A term that rejects sets nothing.";
            rule = ''self.all(t, t["then"].action == "accept" || t["then"].localPref == null && t["then"].med == null && size(t["then"].addCommunities) == 0 && size(t["then"].removeCommunities) == 0 && t["then"].prepend == null)'';
            message = "a term that rejects routes sets local preference, MED, communities or a prepend, which no route it rejects keeps; drop them or accept";
          }
        ];
      };
      default = mkOption {
        type = types.enum [
          "accept"
          "reject"
        ];
        default = "reject";
        description = "What the router does with a route no term matches.";
      };
    };
  };
}
