# DHCP on router images whose chalkos.router.dhcp.enable is true: the subnets the router leases
# addresses of, one document each, which the output kea renders for Kea's DHCPv4 or DHCPv6 server
# of the VRF the subnet's links are in (modules/node/router/dhcp.nix). Kea reads and answers
# DHCPv4 on raw sockets that the firewall does not see, so a subnet names each of its links: no
# glob serves a link by chance.
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists, and costs
# every string as one of 256 characters. So the rules over a subnet's pools and reservations
# against the subnet loop over the documents and one list, and a pool is two addresses rather
# than a string to split; the reservations' MAC addresses and addresses are declared unique.
# Whether a pool's first address comes before its last, and whether a reservation lies outside
# the pools, takes an order of addresses that CEL lacks; the output kea refuses both, as it
# refuses what spans documents.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin groups;
  inherit (import ./router-types.nix { inherit lib; })
    interfaceName
    prefix
    address
    ;

  # A DNS name in lower case without the root's dot, as Kea gives it in options 12, 15 and 119.
  dnsName = types.strMatching "([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?[.])*[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?";
  # A file a PXE client loads: up to 127 printable ASCII characters without spaces, which the
  # 128 bytes of the DHCP header's file field hold.
  bootFile = types.strMatching "[!-~]{1,127}";
  # The rule that each document passes ok, with s as its spec and c as its subnet; the message
  # names the documents that fail, then says what.
  each = description: what: ok: {
    inherit description;
    rule = "documents.all(n, cel.bind(s, documents[n].spec, cel.bind(c, cidr(s.subnet), ${ok})))";
    messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, cel.bind(c, cidr(s.subnet), !(${ok})))).map(n, "DHCPSubnet " + n).join(", ") + " ${what}"'';
  };
  # The rule that the addresses of each document that ok tests lie inside the subnet; one names
  # one of them.
  inside =
    what: one: ok:
    each "A DHCPSubnet's ${what} inside its subnet." "has ${one} outside its subnet" ok;
  ipv4 = ''c.ip(0).family() == "ipv4"'';
in
{
  config = lib.mkIf (config.chalkos.router.enable && config.chalkos.router.dhcp.enable) {
    chalkos.kinds.DHCPSubnet = builtin groups.router // {
      multiple = true;
      description = "A subnet the router's DHCP server leases IPv4 or IPv6 addresses of on its interfaces: pools and reservations, the options its clients get, the files PXE clients boot and, for IPv6, the prefixes it delegates to other routers.";
      rules = [
        (inside "pools lie" "a pool" "s.pools.all(p, c.contains(ip(p.first)) && c.contains(ip(p.last)))")
        (inside "reservations lie" "a reservation" "s.reservations.all(r, c.contains(ip(r.address)))")
        (inside "gateway lies" "a gateway" "s.gateway == null || c.contains(ip(s.gateway))")
        (each "A DHCPSubnet's DNS servers are of its subnet's family."
          "has a DNS server of another family than its subnet"
          "s.dns.all(a, ip(a).family() == c.ip(0).family())"
        )
        (each
          "A gateway is given for IPv4 subnets alone; IPv6 clients learn their routers from router advertisements."
          "gives a gateway to an IPv6 subnet, whose clients learn their routers from router advertisements"
          "s.gateway == null || ${ipv4}"
        )
        (each "PXE boot files are given for IPv4 subnets alone." "gives PXE boot files to an IPv6 subnet"
          "s.boot == null || ${ipv4}"
        )
        (each "A DHCPSubnet delegates prefixes for IPv6 alone." "delegates prefixes in an IPv4 subnet"
          "s.prefixDelegation == null || !(${ipv4})"
        )
        (each "An IPv6 subnet is served on one link, the one Kea's DHCPv6 subnet names."
          "serves an IPv6 subnet on more than one link; give each link a DHCPSubnet of its own"
          "${ipv4} || size(s.interfaces) == 1"
        )
      ];
      spec = {
        interfaces = mkOption {
          type = types.nonEmptyListOf interfaceName;
          example = [ "lan0" ];
          description = "Links the server answers on, each by its full name, without a glob: links of one VRF, each of which a Network document matching it by Name= alone gives a static address in the subnet; one link for an IPv6 subnet. A link takes one subnet of each family.";
        };
        subnet = lib.chalkos.mkOption {
          type = prefix;
          example = "192.0.2.0/24";
          description = "Range the subnet's addresses are in, whose family the server's is: an IPv4 range of 30 bits at most or an IPv6 range of 120 bits at most.";
          rules = [
            {
              description = "A subnet is an IPv4 range of 30 bits at most, outside 0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16 and 224.0.0.0/3, or an IPv6 range of 120 bits at most, outside ::/8, fe80::/10 and ff00::/8.";
              rule = ''cel.bind(c, cidr(self), c.ip(0).family() == "ipv4" ? c.prefix() <= 30 && !["0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3"].exists(r, cidr(r).contains(c.ip(0))) : c.prefix() <= 120 && !["::/8", "fe80::/10", "ff00::/8"].exists(r, cidr(r).contains(c.ip(0))))'';
              messageExpression = ''"the subnet " + self + " is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases"'';
            }
          ];
        };
        pools = mkOption {
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
          description = "Ranges the server leases addresses from to any client, inside the subnet, apart from each other, from the reservations and from the router's, the gateway's and the DNS and boot servers' addresses; empty leases the reservations alone.";
        };
        gateway = mkOption {
          type = types.nullOr address;
          default = null;
          example = "192.0.2.1";
          description = "Router the clients route through, inside an IPv4 subnet and neither its network nor its broadcast address; null gives them none. IPv6 clients learn their routers from router advertisements.";
        };
        dns = mkOption {
          type = types.listOf address;
          default = [ ];
          example = [ "192.0.2.1" ];
          description = "DNS servers the clients ask, addresses of the subnet's family.";
        };
        domain = mkOption {
          type = types.nullOr dnsName;
          default = null;
          example = "lan.example.org";
          description = "Domain of the clients' names, which they also search; null gives none.";
        };
        leaseTime = mkOption {
          type = types.strMatching "[1-9][0-9]{0,5}[smhd]";
          default = "12h";
          example = "1d";
          description = "How long a lease lasts, and a delegated prefix: a number and s, m, h or d, 136 years at most. IPv6 clients prefer their addresses for as long.";
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
                  description = "Address the client always gets, inside the subnet and outside its pools, neither the router's nor the gateway's nor a DNS or boot server's.";
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
                  description = "IPv6 range the delegated prefixes are taken from, apart from the subnets of the VRF.";
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
              description = "Prefixes are delegated from an IPv6 range, each longer than the range.";
              rule = ''self == null || (cidr(self.prefix).ip(0).family() == "ipv6" && !(self.length <= cidr(self.prefix).prefix()))'';
              messageExpression = ''"prefixDelegation's prefix " + self.prefix + " is no IPv6 range shorter than " + string(self.length) + " bits"'';
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
