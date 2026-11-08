# DHCP on router images whose chalkos.router.dhcp.enable is true: the subnets the router leases
# addresses of, one document each, which the output kea renders for Kea's DHCPv4 or DHCPv6 server
# of the VRF the subnet's links are in (default.nix). Kea reads and answers DHCPv4 on raw sockets
# that the firewall does not see, so a subnet names each of its links: no glob serves a link by
# chance.
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists, and costs
# every string as one of 256 characters. A subnet's rules are rules of its spec, which see one
# document at a time, so a rule may compare two of its lists, as its reservations with its pools,
# within that limit; a pool is two addresses rather than a string to split, and ip().compare()
# orders them. chalkd stops a rule at a cost of 1,000,000 on the config it meets, so the lists
# those rules pair are bounded: 32 pools, 512 reservations and 16 DNS servers, at which the
# costliest rule, reservations against pools, costs about 330,000, and its message as much. The
# reservations' MAC addresses and addresses are declared unique. The validator router refuses
# what spans documents: a link of two subnets of a family, subnets that overlap in a VRF, the
# router's own addresses on the links in a pool or reservation, and the links' router
# advertisements.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (import ../../kinds/builtin/lib.nix) builtin groups;
  inherit (import ../types.nix { inherit lib; })
    interfaceName
    prefix
    address
    ;

  # A DNS name in lower case without the root's dot, as Kea gives it in options 12, 15 and 119.
  dnsName = types.strMatching "([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?[.])*[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?";
  # A file a PXE client loads: up to 127 printable ASCII characters without spaces, which the
  # 128 bytes of the DHCP header's file field hold.
  bootFile = types.strMatching "[!-~]{1,127}";
  # The expression e of a subnet's spec, self, with c bound to the subnet's range.
  ofSubnet = e: "cel.bind(c, cidr(self.subnet), ${e})";
  # The rule of a subnet's spec that holds when ok does, whose message says what fails; both read
  # c as ofSubnet binds it.
  subnetRule = description: ok: message: {
    inherit description;
    rule = ofSubnet ok;
    messageExpression = ofSubnet message;
  };
  # The expression e with x bound to the first item of the list l that ok does not hold for.
  firstFailing =
    x: l: ok: e:
    "cel.bind(${x}, ${l}.filter(${x}, !(${ok}))[0], ${e})";
  ipv4 = ''c.ip(0).family() == "ipv4"'';
  # Whether the address a is the subnet's first address, an IPv4 subnet's network address and an
  # IPv6 subnet's subnet-router anycast address, or an IPv4 subnet's last, its broadcast address:
  # no client takes them.
  edge = a: "(ip(${a}) == c.ip(0) || ${ipv4} && ip(${a}) == c.last())";
  # What the address a, which edge holds for, is.
  edgeName =
    a:
    ''(ip(${a}) != c.ip(0) ? "broadcast address" : ${ipv4} ? "network address" : "subnet-router anycast address")'';
  # Whether the pool p holds the address a, and whether the pools p and q share an address.
  holds = p: a: "(ip(${p}.first).compare(ip(${a})) <= 0 && ip(${a}).compare(ip(${p}.last)) <= 0)";
  overlap =
    p: q: "(ip(${p}.first).compare(ip(${q}.last)) <= 0 && ip(${q}.first).compare(ip(${p}.last)) <= 0)";
  # The servers of the subnet's clients, each an address and what it is, which no pool or
  # reservation holds: the gateway, the DNS servers and boot's server.
  servers = ''(self.gateway == null ? [] : [{"address": self.gateway, "is": "the gateway"}]) + self.dns.map(a, {"address": a, "is": "a DNS server"}) + (self.boot == null || self.boot.server == null ? [] : [{"address": self.boot.server, "is": "boot's server"}])'';
  # Whether the range c overlaps one of the ranges rs, as either holds the other's first address.
  overlapsAny =
    c: rs: "${rs}.exists(r, cidr(r).contains(${c}.ip(0)) || ${c}.contains(cidr(r).ip(0)))";
  # The ranges of each family that no server leases or delegates addresses of: unspecified,
  # loopback, link-local and multicast addresses, and IPv4's reserved ones above them.
  special4 = ''["0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3"]'';
  special6 = ''["::/8", "fe80::/10", "ff00::/8"]'';
  # How a message names the pool p and the reservation r.
  pool = p: ''"the pool " + ${p}.first + "-" + ${p}.last'';
  reservation = r: ''"the reservation of " + ${r}.mac + ", " + ${r}.address + ","'';
in
{
  config = lib.mkIf (config.chalkos.router.enable && config.chalkos.router.dhcp.enable) {
    chalkos.kinds.DHCPSubnet = builtin groups.router // {
      multiple = true;
      description = "A subnet the router's DHCP server leases IPv4 or IPv6 addresses of on its interfaces: pools and reservations, the options its clients get, the files PXE clients boot and, for IPv6, the prefixes it delegates to other routers.";
      specRules = [
        (subnetRule "A pool lies inside the subnet."
          "self.pools.all(p, c.contains(ip(p.first)) && c.contains(ip(p.last)))"
          (
            firstFailing "p" "self.pools" "c.contains(ip(p.first)) && c.contains(ip(p.last))"
              ''${pool "p"} + " lies outside the subnet " + string(c)''
          )
        )
        {
          description = "A pool's first address comes before its last or is its last.";
          rule = "self.pools.all(p, ip(p.first).compare(ip(p.last)) <= 0)";
          messageExpression =
            firstFailing "p" "self.pools" "ip(p.first).compare(ip(p.last)) <= 0"
              ''${pool "p"} + " ends before it starts"'';
        }
        {
          description = "No two pools share an address.";
          rule = "self.pools.all(p, size(self.pools.filter(q, ${overlap "p" "q"})) <= 1)";
          messageExpression =
            firstFailing "p" "self.pools" "size(self.pools.filter(q, ${overlap "p" "q"})) <= 1"
              ''${pool "p"} + " shares addresses with another pool"'';
        }
        (subnetRule
          "A pool leaves out the subnet's network and broadcast addresses, or an IPv6 subnet's subnet-router anycast address."
          "self.pools.all(p, !${edge "p.first"} && !${edge "p.last"})"
          (
            firstFailing "p" "self.pools" "!${edge "p.first"} && !${edge "p.last"}"
              ''cel.bind(a, ${edge "p.first"} ? p.first : p.last, ${pool "p"} + " holds " + a + ", the " + ${edgeName "a"} + " of the subnet " + string(c))''
          )
        )
        {
          description = "A pool holds neither the gateway nor a DNS server nor boot's server.";
          rule = "cel.bind(t, ${servers}, self.pools.all(p, t.all(x, !${holds "p" "x.address"})))";
          messageExpression = "cel.bind(t, ${servers}, ${
            firstFailing "p" "self.pools" "t.all(x, !${holds "p" "x.address"})" (
              firstFailing "x" "t" "!${holds "p" "x.address"}"
                ''${pool "p"} + " holds " + x.address + ", " + x.is''
            )
          })";
        }
        (subnetRule "A reservation lies inside the subnet."
          "self.reservations.all(r, c.contains(ip(r.address)))"
          (
            firstFailing "r" "self.reservations" "c.contains(ip(r.address))"
              ''${reservation "r"} + " lies outside the subnet " + string(c)''
          )
        )
        {
          description = "A reservation lies outside the pools.";
          rule = "self.reservations.all(r, self.pools.all(p, !${holds "p" "r.address"}))";
          messageExpression =
            firstFailing "r" "self.reservations" "self.pools.all(p, !${holds "p" "r.address"})"
              (
                firstFailing "p" "self.pools" "!${holds "p" "r.address"}"
                  ''${reservation "r"} + " lies in " + ${pool "p"} + "; reserve addresses outside the pools"''
              );
        }
        (subnetRule
          "A reservation is neither the subnet's network nor its broadcast address, nor an IPv6 subnet's subnet-router anycast address."
          "self.reservations.all(r, !${edge "r.address"})"
          (
            firstFailing "r" "self.reservations" "!${edge "r.address"}"
              ''${reservation "r"} + " is the " + ${edgeName "r.address"} + " of the subnet " + string(c)''
          )
        )
        {
          description = "A reservation is neither the gateway nor a DNS server nor boot's server.";
          rule = "cel.bind(t, ${servers}, self.reservations.all(r, t.all(x, ip(x.address) != ip(r.address))))";
          messageExpression = "cel.bind(t, ${servers}, ${
            firstFailing "r" "self.reservations" "t.all(x, ip(x.address) != ip(r.address))" (
              firstFailing "x" "t" "ip(x.address) != ip(r.address)" ''${reservation "r"} + " is " + x.is''
            )
          })";
        }
        {
          description = "A gateway is given for IPv4 subnets alone; IPv6 clients learn their routers from router advertisements.";
          rule = ofSubnet "self.gateway == null || ${ipv4}";
          message = "gateway is for IPv4 subnets; IPv6 clients learn their routers from router advertisements";
        }
        (subnetRule "A gateway lies inside the subnet."
          "self.gateway == null || c.contains(ip(self.gateway))"
          ''"the gateway " + self.gateway + " lies outside the subnet " + string(c)''
        )
        (subnetRule "A gateway is neither the subnet's network nor its broadcast address."
          "self.gateway == null || !(${ipv4}) || !${edge "self.gateway"}"
          ''"the gateway, " + self.gateway + ", is the " + ${edgeName "self.gateway"} + " of the subnet " + string(c)''
        )
        (subnetRule "A DNS server is an address of the subnet's family, and no IPv4-mapped IPv6 address."
          ''self.dns.all(a, ip(a).family() == c.ip(0).family() && !cidr("::ffff:0:0/96").contains(ip(a)))''
          (
            firstFailing "a" "self.dns"
              ''ip(a).family() == c.ip(0).family() && !cidr("::ffff:0:0/96").contains(ip(a))''
              ''"the DNS server " + a + " is no " + (${ipv4} ? "IPv4" : "IPv6") + " address"''
          )
        )
        {
          description = "PXE boot files are given for IPv4 subnets alone.";
          rule = ofSubnet "self.boot == null || ${ipv4}";
          message = "boot is for IPv4 subnets";
        }
        {
          description = "A subnet delegates prefixes for IPv6 alone.";
          rule = ofSubnet "self.prefixDelegation == null || !(${ipv4})";
          message = "prefixDelegation delegates IPv6 prefixes, which an IPv4 subnet has none of";
        }
        (subnetRule "The prefixes a subnet delegates lie apart from the subnet."
          "self.prefixDelegation == null || cel.bind(d, cidr(self.prefixDelegation.prefix), !c.contains(d.ip(0)) && !d.contains(c.ip(0)))"
          ''"prefixDelegation's prefix " + self.prefixDelegation.prefix + " overlaps the subnet " + string(c)''
        )
        (subnetRule "An IPv6 subnet is served on one link, the one Kea's DHCPv6 subnet names."
          "${ipv4} || size(self.interfaces) == 1"
          ''"an IPv6 subnet is served on one link, the one Kea's subnet names; give each of " + self.interfaces.join(", ") + " a DHCPSubnet of its own"''
        )
      ];
      spec = {
        interfaces = lib.chalkos.mkOption {
          type = types.nonEmptyListOf interfaceName;
          unique = {
            across = "list";
            hint = "name each link once";
          };
          example = [ "lan0" ];
          description = "Links the server answers on, each by its full name, without a glob, and once: links of one VRF, each of which a Network document matching it by Name= alone gives a static address in the subnet; one link for an IPv6 subnet. A link takes one subnet of each family.";
        };
        subnet = lib.chalkos.mkOption {
          type = prefix;
          example = "192.0.2.0/24";
          description = "Range the subnet's addresses are in, whose family the server's is: an IPv4 range of 30 bits at most or an IPv6 range of 120 bits at most.";
          rules = [
            {
              description = "A subnet is an IPv4 range of 30 bits at most apart from 0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16 and 224.0.0.0/3, or an IPv6 range of 120 bits at most apart from ::/8, fe80::/10 and ff00::/8.";
              rule = ''cel.bind(c, cidr(self), c.ip(0).family() == "ipv4" ? c.prefix() <= 30 && !${overlapsAny "c" special4} : c.prefix() <= 120 && !${overlapsAny "c" special6})'';
              messageExpression = ''"the subnet " + self + " is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases"'';
            }
          ];
        };
        pools = lib.chalkos.mkOption {
          type = types.listOf (
            types.submodule {
              options = {
                first = mkOption {
                  type = address;
                  description = "First address of the pool.";
                };
                last = mkOption {
                  type = address;
                  description = "Last address of the pool, no lower than the first.";
                };
              };
            }
          );
          default = [ ];
          example = [
            {
              first = "192.0.2.100";
              last = "192.0.2.199";
            }
          ];
          description = "Ranges the server leases addresses from to any client: inside the subnet, apart from each other and from the reservations, and holding none of the subnet's network and broadcast addresses, an IPv6 subnet's first address and the router's, the gateway's and the DNS and boot servers' addresses; empty leases the reservations alone.";
          maxItems = 32;
        };
        gateway = mkOption {
          type = types.nullOr address;
          default = null;
          example = "192.0.2.1";
          description = "Router the clients route through, inside an IPv4 subnet and neither its network nor its broadcast address; null gives them none. IPv6 clients learn their routers from router advertisements.";
        };
        dns = lib.chalkos.mkOption {
          type = types.listOf address;
          default = [ ];
          maxItems = 16;
          example = [ "192.0.2.1" ];
          description = "DNS servers the clients ask, addresses of the subnet's family.";
        };
        domain = mkOption {
          type = types.nullOr dnsName;
          default = null;
          example = "lan.example.org";
          description = "Domain of the clients' names, which they also search; null gives none.";
        };
        leaseTime = lib.chalkos.mkOption {
          type = types.strMatching "[1-9][0-9]{0,5}[smhd]";
          default = "12h";
          example = "1d";
          description = "How long a lease lasts, and a delegated prefix: a number and s, m, h or d, 136 years at most. IPv6 clients prefer their addresses for as long.";
          # Kea takes a lease's seconds as a 32-bit number. Of the units the schema takes, only
          # days reach past it: 999999h is 3599996400 seconds.
          rules = [
            {
              description = "A lease lasts 4294967295 seconds at most, the longest Kea takes.";
              rule = ''int(self.substring(0, size(self) - 1)) * {"s": 1, "m": 60, "h": 3600, "d": 86400}[self.substring(size(self) - 1)] <= 4294967295'';
              messageExpression = ''"a lease of " + self + " is longer than 4294967295 seconds, the longest Kea takes"'';
            }
          ];
        };
        reservations = lib.chalkos.mkOption {
          type = types.listOf (
            types.submodule {
              options = {
                mac = mkOption {
                  type = types.strMatching "[0-9a-f]{2}(:[0-9a-f]{2}){5}";
                  example = "52:54:00:12:34:56";
                  description = "MAC address of the client, in lower case. A DHCPv6 server learns it from the client's DUID or link-local address, which not every client derives from it.";
                };
                address = mkOption {
                  type = address;
                  example = "192.0.2.10";
                  description = "Address the client always gets, inside the subnet and outside its pools, and none of the subnet's network and broadcast addresses, an IPv6 subnet's first address and the router's, the gateway's and the DNS and boot servers' addresses.";
                };
                hostname = mkOption {
                  type = types.nullOr dnsName;
                  default = null;
                  example = "printer";
                  description = "Name the client gets; null gives none.";
                };
              };
            }
          );
          default = [ ];
          description = "Clients that always get the same address, by their MAC address.";
          maxItems = 512;
          # A MAC address has one reservation, and an address is reserved once, in whatever
          # spelling.
          unique = [
            {
              by = [
                {
                  field = "mac";
                  as = "lower";
                }
              ];
              hint = "keep one reservation of the MAC address";
            }
            {
              by = [
                {
                  field = "address";
                  as = "ip";
                }
              ];
              hint = "reserve each address once";
            }
          ];
        };
        prefixDelegation = lib.chalkos.mkOption {
          type = types.nullOr (
            types.submodule {
              options = {
                prefix = mkOption {
                  type = prefix;
                  example = "2001:db8:100::/56";
                  description = "IPv6 range the delegated prefixes are taken from, apart from ::/8, fe80::/10, ff00::/8 and the subnets of the VRF.";
                };
                length = mkOption {
                  type = types.ints.between 1 128;
                  example = 64;
                  description = "Length of each delegated prefix, longer than the range's.";
                };
              };
            }
          );
          default = null;
          description = "Prefixes the IPv6 server delegates to routers on the link that ask for one, such as a home router's LAN; the router routes each to the link-local address that asked for it, for as long as the lease lasts. null delegates none.";
          rules = [
            {
              description = "Prefixes are delegated from an IPv6 range apart from ::/8, fe80::/10 and ff00::/8, each longer than the range.";
              rule = ''self == null || cel.bind(d, cidr(self.prefix), d.ip(0).family() == "ipv6" && !${overlapsAny "d" special6} && self.length > d.prefix())'';
              messageExpression = ''"prefixDelegation's prefix " + self.prefix + " is no IPv6 range shorter than " + string(self.length) + " bits apart from ::/8, fe80::/10 and ff00::/8"'';
            }
          ];
        };
        boot = lib.chalkos.mkOption {
          type = types.nullOr (
            types.submodule {
              options = {
                server = mkOption {
                  type = types.nullOr address;
                  default = null;
                  example = "192.0.2.5";
                  description = "IPv4 address of the TFTP server the files are on, the DHCP header's next server; needed with bios or uefi, whose firmware loads them over TFTP. null for none, as for an ipxe script of an HTTP URL alone.";
                };
                bios = mkOption {
                  type = types.nullOr bootFile;
                  default = null;
                  example = "undionly.kpxe";
                  description = "File of clients with BIOS firmware, of architecture 0 in option 93, loaded over TFTP from server.";
                };
                uefi = mkOption {
                  type = types.nullOr bootFile;
                  default = null;
                  example = "ipxe.efi";
                  description = "File of clients with x86-64 UEFI firmware, of architecture 7 or 9 in option 93, loaded over TFTP from server.";
                };
                ipxe = mkOption {
                  type = types.nullOr bootFile;
                  default = null;
                  example = "http://192.0.2.5/boot.ipxe";
                  description = "Script or file of clients that run iPXE already, whose user class (option 77) is iPXE; they get it instead of bios and uefi, so that iPXE does not load itself again.";
                };
              };
            }
          );
          default = null;
          description = "Files PXE clients of an IPv4 subnet boot, by firmware; boot files are served elsewhere. null serves none.";
          rules = [
            {
              description = "A boot names a file.";
              rule = "self == null || self.bios != null || self.uefi != null || self.ipxe != null";
              message = "boot names no file; give bios, uefi or ipxe, or leave boot out";
            }
            {
              description = "A boot's server is an IPv4 address.";
              rule = ''self == null || self.server == null || ip(self.server).family() == "ipv4"'';
              message = "boot's server is no IPv4 address";
            }
            {
              description = "A boot of BIOS or UEFI files names their server.";
              rule = "self == null || self.server != null || (self.bios == null && self.uefi == null)";
              message = "boot's bios and uefi files load over TFTP from boot's server; give server";
            }
          ];
        };
      };
    };
  };
}
