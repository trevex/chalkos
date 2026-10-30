# BGP on images with the router layer: a BGP instance per VRF, its peers, and the prefix lists,
# community lists and routing policies peers, networks and imports name, which the output frr
# renders into FRR's config (modules/node/router/bgp.nix).
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists. So the
# rules over a peer's families sit on spec.families and those over an instance's networks and
# imports on spec.networks and spec.import, where the one document is self, and loop over a
# policy's terms alone.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin groups;
  inherit (import ../../../../lib/kinds/types.nix { inherit lib; })
    documentName
    peerAS
    secretRef
    ;
  inherit (import ./router-types.nix { inherit lib; })
    interface
    interfaceText
    interfaceName
    prefix
    address
    family
    delivered
    vrfOf
    ;

  name = types.strMatching documentName;
  # A number from 1 to 2^32 - 1, as AS numbers and BGP's counts are.
  positive32 = types.ints.between 1 4294967295;
  # Numbers of 16 and of 32 bits in decimal, as a community's parts are.
  uint16 = "[0-5]?[0-9]{1,4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5]";
  uint32 = "[0-9]{1,9}|[0-3][0-9]{9}|4[01][0-9]{8}|42[0-8][0-9]{7}|429[0-3][0-9]{6}|4294[0-8][0-9]{5}|42949[0-5][0-9]{4}|429496[0-6][0-9]{3}|4294967[01][0-9]{2}|42949672[0-8][0-9]|429496729[0-5]";
  # A standard community, <AS>:<value>, or a large one, <AS>:<value>:<value>.
  community = types.strMatching "(${uint16}):(${uint16})|(${uint32}):(${uint32}):(${uint32})";
  communityText = "standard communities `<AS>:<value>` of 16-bit numbers, or large ones `<AS>:<value>:<value>` of 32-bit numbers";
  # An octet of an IPv4 address in decimal, without leading zeros.
  octet = "25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9]";
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

  # Whether the PrefixList entry e is the one of its list, self, that matches what it matches.
  sameEntry = "self.exists_one(x, cidr(x.prefix) == cidr(e.prefix) && x.ge == e.ge && x.le == e.le)";

  # The vrf of the running BGP document of the name n, null for the default VRF; a running config
  # of an image whose BGP had no vrf lacks the field.
  oldVRF = n: "(has(oldDocuments[${n}].spec.vrf) ? oldDocuments[${n}].spec.vrf : null)";
  # The VRFs the config's BGP documents serve.
  served = "cel.bind(bgps, ${delivered "BGP"}, bgps.map(n, ${vrfOf "bgps[n]"}))";
  # The interfaces and globs of the config's VRF documents, as written, each as [interface, VRF].
  links = "cel.bind(v, ${delivered "VRF"}, v.map(n, v[n].spec.interfaces.map(i, [i, n])).flatten())";
  # Whether the BGPPeer spec s names no interface, or one that held gives no VRF but the peer's.
  ownLink = "(s.interface == null || held.all(h, h[0] != s.interface || h[1] == s.vrf))";
  # What a BGP instance imports of one family from other VRFs' instances.
  leak = types.submodule {
    options = {
      vrfs = mkOption {
        # A VRF document's name or the default VRF's, as an import names it.
        type = types.nonEmptyListOf name;
        example = [ "tenant" ];
        description = "VRFs whose BGP instances' routes of the family the instance takes, each once, by the names of their VRF documents or `default`.";
      };
      policy = mkOption {
        type = types.nullOr name;
        default = null;
        description = "RoutingPolicy that decides the routes the instance takes from all of `vrfs`; null takes every route.";
      };
    };
  };
  # Whether the RoutingPolicy p, if the config holds it, matches PrefixLists of the family f: the
  # first it names that the config holds is of f, as the policy's other lists are by a rule of
  # its own. It reads the config's RoutingPolicy documents as pols and its PrefixList documents
  # as lists.
  policyOfFamily =
    p: f:
    "(!(${p} in pols) || cel.bind(named, pols[${p}].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == ${f}))";
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

  # What a peer does with the routes of one address family.
  peerFamily = types.submodule {
    options = {
      import = mkOption {
        type = types.nullOr name;
        default = null;
        description = "RoutingPolicy that decides the routes the router accepts from the peer; null accepts every route.";
      };
      export = mkOption {
        type = types.nullOr name;
        default = null;
        description = "RoutingPolicy that decides the routes the router sends the peer; null sends every route the router has.";
      };
      defaultOriginate = mkOption {
        type = types.bool;
        default = false;
        description = "Whether the router sends the peer a default route of the family, whatever its own table holds.";
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
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.kinds = {
      BGP = builtin groups.router // {
        multiple = true;
        description = "A BGP instance of the router in one VRF: its AS number and router ID, how many equal paths it installs, its BFD timers, the networks it announces and the routes it imports from other VRFs.";
        rules = [
          {
            description = "One BGP document serves a VRF.";
            rule = "cel.bind(v, documents.map(n, ${vrfOf "documents[n]"}), v.all(x, size(v.filter(y, y == x)) == 1))";
            messageExpression = ''cel.bind(v, documents.map(n, ${vrfOf "documents[n]"}), "more than one BGP document serves the VRF " + v.filter(x, size(v.filter(y, y == x)) > 1)[0] + "; a VRF has one BGP instance")'';
          }
          {
            description = "A BGP document's vrf is a VRF document.";
            rule = "cel.bind(vrfs, ${delivered "VRF"}, documents.all(n, documents[n].spec.vrf == null || documents[n].spec.vrf in vrfs))";
            messageExpression = ''cel.bind(vrfs, ${delivered "VRF"}, documents.filter(n, documents[n].spec.vrf != null && !(documents[n].spec.vrf in vrfs)).map(n, "BGP " + n + " names the VRF " + documents[n].spec.vrf).join(", ") + ", which no VRF document declares; leave vrf out for the default VRF")'';
          }
          {
            description = "A BGP instance does not import its own VRF.";
            rule = ''documents.all(n, cel.bind(s, documents[n].spec, [s["import"].ipv4, s["import"].ipv6].all(f, f == null || !(${vrfOf "documents[n]"} in f.vrfs))))'';
            messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, ![s["import"].ipv4, s["import"].ipv6].all(f, f == null || !(${vrfOf "documents[n]"} in f.vrfs)))).map(n, "BGP " + n).join(", ") + " imports the routes of its own VRF; name other VRFs"'';
          }
          {
            description = "A delivery that drops the default VRF's BGP document keeps no running BGP instance of another VRF, as FRR keeps its default instance while another runs.";
            rule = "!oldDocuments.exists(n, ${oldVRF "n"} == null) || documents.exists(n, documents[n].spec.vrf == null) || !documents.exists(n, documents[n].spec.vrf != null && oldDocuments.exists(m, ${oldVRF "m"} == documents[n].spec.vrf))";
            message = "the config drops the default VRF's BGP document and keeps a running BGP instance of another VRF, which FRR does not allow; drop the other VRFs' BGP documents in the same delivery, or keep the default VRF's";
          }
        ];
        spec = {
          vrf = mkOption {
            type = types.nullOr name;
            default = null;
            example = "tenant";
            description = "VRF document whose instance this is; null for the default VRF, which holds every link no VRF holds.";
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
          routerID = mkOption {
            type = types.strMatching "(${octet})([.](${octet})){3}";
            example = "192.0.2.1";
            description = "Router ID, in IPv4 notation, which the router's peers know it by; give an IPv6-only router one too.";
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
                    type = types.nullOr name;
                    default = null;
                    description = "RoutingPolicy whose terms set the announcement's attributes; null announces it as it is.";
                  };
                };
              }
            );
            default = [ ];
            description = "Networks the router announces to its peers.";
            rules = [
              {
                description = "A network's prefix is an address range.";
                rule = "self.all(w, cidr(w.prefix).prefix() >= 0)";
                message = "a network is no address range";
              }
              {
                description = "A network's policy is a RoutingPolicy document.";
                rule = "cel.bind(pols, ${delivered "RoutingPolicy"}, self.all(w, w.policy == null || w.policy in pols))";
                messageExpression = ''cel.bind(pols, ${delivered "RoutingPolicy"}, "a network names the RoutingPolicy " + self.filter(w, w.policy != null && !(w.policy in pols))[0].policy + ", which the config does not hold")'';
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
                description = "An import names each VRF once.";
                rule = "[self.ipv4, self.ipv6].all(f, f == null || f.vrfs.all(v, size(f.vrfs.filter(w, w == v)) == 1))";
                message = "the import names a VRF twice";
              }
              {
                description = "An import's policy is a RoutingPolicy document.";
                rule = "cel.bind(pols, ${delivered "RoutingPolicy"}, [self.ipv4, self.ipv6].all(f, f == null || f.policy == null || f.policy in pols))";
                messageExpression = ''cel.bind(pols, ${delivered "RoutingPolicy"}, "an import names the RoutingPolicy " + [self.ipv4, self.ipv6].filter(f, f != null && f.policy != null && !(f.policy in pols))[0].policy + ", which the config does not hold")'';
              }
              {
                description = "An import's policy matches PrefixLists of its family alone.";
                rule = "cel.bind(pols, ${delivered "RoutingPolicy"}, cel.bind(lists, ${delivered "PrefixList"}, (self.ipv4 == null || self.ipv4.policy == null || ${policyOfFamily "self.ipv4.policy" ''"ipv4"''}) && (self.ipv6 == null || self.ipv6.policy == null || ${policyOfFamily "self.ipv6.policy" ''"ipv6"''})))";
                messageExpression = ''cel.bind(pols, ${delivered "RoutingPolicy"}, cel.bind(lists, ${delivered "PrefixList"}, "the " + (self.ipv4 == null || self.ipv4.policy == null || ${policyOfFamily "self.ipv4.policy" ''"ipv4"''} ? "ipv6" : "ipv4") + " import's RoutingPolicy matches PrefixLists of another family"))'';
              }
            ];
          };
        };
      };

      BGPPeer = builtin groups.router // {
        multiple = true;
        description = "A BGP neighbour of the router, or the neighbours on a set of interfaces: where to reach them, their AS, and what the router accepts from them and sends them in each address family.";
        rules = [
          {
            description = "A BGPPeer names exactly one of address, interface and interfaces.";
            rule = "documents.all(n, cel.bind(s, documents[n].spec, [s.address, s.interface, s.interfaces].filter(x, x != null).size() == 1))";
            messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, [s.address, s.interface, s.interfaces].filter(x, x != null).size() != 1)).map(n, "BGPPeer " + n).join(", ") + " names not exactly one of address, interface and interfaces; a peer is reached at an address, on an interface or on each interface of a list"'';
          }
          {
            description = "A BGPPeer's vrf is a VRF document.";
            rule = "cel.bind(vrfs, ${delivered "VRF"}, documents.all(n, documents[n].spec.vrf == null || documents[n].spec.vrf in vrfs))";
            messageExpression = ''cel.bind(vrfs, ${delivered "VRF"}, documents.filter(n, documents[n].spec.vrf != null && !(documents[n].spec.vrf in vrfs)).map(n, "BGPPeer " + n + " names the VRF " + documents[n].spec.vrf).join(", ") + ", which no VRF document declares; leave vrf out for the default VRF")'';
          }
          {
            description = "A BGPPeer's VRF has a BGP document.";
            rule = "cel.bind(have, ${served}, documents.all(n, ${vrfOf "documents[n]"} in have))";
            messageExpression = ''cel.bind(have, ${served}, documents.filter(n, !(${vrfOf "documents[n]"} in have)).map(n, "BGPPeer " + n + " runs in the VRF " + ${vrfOf "documents[n]"}).join(", ") + ": a peer's VRF needs a BGP document, which gives the router its AS number there")'';
          }
          {
            description = "A BGPPeer's interface is no link another VRF than the peer's holds; names compare as written.";
            rule = "cel.bind(held, ${links}, documents.all(n, cel.bind(s, documents[n].spec, ${ownLink})))";
            messageExpression = ''cel.bind(held, ${links}, documents.filter(n, cel.bind(s, documents[n].spec, !${ownLink})).map(n, "BGPPeer " + n + " names the interface " + documents[n].spec.interface).join(", ") + ", which a VRF document puts in another VRF than the peer's; give the peer that VRF or the interface another")'';
          }
        ];
        spec = {
          vrf = mkOption {
            type = types.nullOr name;
            default = null;
            example = "tenant";
            description = "VRF document whose BGP instance runs the session; null for the default VRF. The peer's interfaces are links of that VRF.";
          };
          address = lib.chalkos.mkOption {
            type = types.nullOr address;
            default = null;
            example = "2001:db8:ffff::2";
            description = "Unicast address of the neighbour; excludes `interface` and `interfaces`.";
            rules = [
              {
                description = "A BGPPeer's address is a unicast address.";
                rule = ''self == null || cel.bind(a, ip(self), a != ip("0.0.0.0") && a != ip("::") && !cidr("224.0.0.0/4").contains(a) && !cidr("ff00::/8").contains(a))'';
                messageExpression = ''"the address " + self + " is no neighbour's: give a unicast address"'';
              }
            ];
          };
          interface = lib.chalkos.mkOption {
            type = types.nullOr interfaceName;
            default = null;
            example = "eth1";
            description = "Interface on whose link the router finds the neighbour by its IPv6 link-local address, without an address of its own on the link; excludes `address` and `interfaces`. A name that reads as an address is refused.";
            rules = [ (notAddressLike "[self]") ];
          };
          interfaces = lib.chalkos.mkOption {
            type = types.nullOr (types.nonEmptyListOf interface);
            default = null;
            example = [
              "eth1"
              "eth2"
            ];
            description = "Interfaces, ${interfaceText}, with a session each, as for `interface`; a glob stands for the interfaces of the peer's VRF that the node's Network documents name in a `[Match]` section's `Name=`. Excludes `address` and `interface`.";
            rules = [ (notAddressLike "self") ];
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
            description = "Whether the router sends and accepts IPv4 routes with IPv6 next hops (RFC 8950). FRR negotiates it on sessions of `interface` and `interfaces` whatever this says.";
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
                description = "A family's policies are RoutingPolicy documents.";
                rule = "cel.bind(pols, ${delivered "RoutingPolicy"}, ${eachFamilyPolicy (p: _: "${p} in pols")})";
                messageExpression = ''cel.bind(pols, ${delivered "RoutingPolicy"}, cel.bind(u, ${
                  firstFamilyPolicy (p: _: "${p} in pols")
                }, "the " + u[1] + " family names the RoutingPolicy " + u[0] + ", which the config does not hold"))'';
              }
              {
                description = "A family's policies match PrefixLists of that family alone.";
                rule = "cel.bind(pols, ${delivered "RoutingPolicy"}, cel.bind(lists, ${delivered "PrefixList"}, ${eachFamilyPolicy policyOfFamily}))";
                messageExpression = ''cel.bind(pols, ${delivered "RoutingPolicy"}, cel.bind(lists, ${delivered "PrefixList"}, cel.bind(u, ${firstFamilyPolicy policyOfFamily}, "the " + u[1] + " family's RoutingPolicy " + u[0] + " matches a PrefixList of another family")))'';
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
            rules = [
              {
                description = "A PrefixList's entries match apart: no two have one prefix, ge and le.";
                rule = "self.all(e, ${sameEntry})";
                messageExpression = ''cel.bind(d, self.filter(e, !${sameEntry})[0], "two entries match " + string(cidr(d.prefix)) + (d.ge != null ? " ge " + string(d.ge) : "") + (d.le != null ? " le " + string(d.le) : "") + ", and the later never decides; drop one")'';
              }
            ];
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
        spec.communities = mkOption {
          type = types.nonEmptyListOf community;
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
                      type = types.nullOr name;
                      default = null;
                      description = "PrefixList the route's prefix matches; null matches every prefix.";
                    };
                    communityList = mkOption {
                      type = types.nullOr name;
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
                description = "A term's lists are PrefixList and CommunityList documents.";
                rule = "cel.bind(lists, ${delivered "PrefixList"}, cel.bind(clists, ${delivered "CommunityList"}, self.all(t, (t.match.prefixList == null || t.match.prefixList in lists) && (t.match.communityList == null || t.match.communityList in clists))))";
                messageExpression = ''cel.bind(lists, ${delivered "PrefixList"}, cel.bind(clists, ${delivered "CommunityList"}, cel.bind(m, self.filter(t, !((t.match.prefixList == null || t.match.prefixList in lists) && (t.match.communityList == null || t.match.communityList in clists)))[0].match, m.prefixList != null && !(m.prefixList in lists) ? "a term names the PrefixList " + m.prefixList + ", which the config does not hold" : "a term names the CommunityList " + m.communityList + ", which the config does not hold")))'';
              }
              {
                description = "A term's asPath holds 200 characters at most.";
                rule = "self.all(t, t.match.asPath == null || size(t.match.asPath) <= 200)";
                message = "a term's asPath holds more than 200 characters";
              }
              {
                description = "A RoutingPolicy's PrefixLists are of one family.";
                rule = "cel.bind(lists, ${delivered "PrefixList"}, cel.bind(families, self.filter(t, t.match.prefixList != null && t.match.prefixList in lists).map(t, lists[t.match.prefixList].spec.family), families.all(f, f == families[0])))";
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
    };
  };
}
