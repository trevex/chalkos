# DHCP on router images whose chalkos.router.dhcp.enable is true: the subnets the router leases
# addresses of, one document each, which the output kea renders for the DHCPv4 server, Kea, of the
# VRF the subnet's links are in (modules/node/router/dhcp.nix). Kea reads and answers DHCPv4 on
# raw sockets that the firewall does not see, so a subnet names each of its links: no glob serves
# a link by chance.
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists, and costs
# every string as one of 256 characters. So the rules over a subnet's reservations in pairs sit on
# spec.reservations, the rules over its pools and reservations against the subnet loop over the
# documents and one list, and a pool is two addresses rather than a string to split. Whether a
# pool's first address comes before its last, and whether a reservation lies outside the pools,
# takes an order of addresses that CEL lacks; the output kea refuses both, as it refuses what spans
# documents.
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
  # The rule that the addresses of each document that ok tests, with s as the spec and c as its
  # subnet, lie inside the subnet; one names one of them.
  inside = what: one: ok: {
    description = "A DHCPSubnet's ${what} inside its subnet.";
    rule = "documents.all(n, cel.bind(s, documents[n].spec, cel.bind(c, cidr(s.subnet), ${ok})))";
    messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, cel.bind(c, cidr(s.subnet), !(${ok})))).map(n, "DHCPSubnet " + n).join(", ") + " has ${one} outside its subnet"'';
  };
in
{
  config = lib.mkIf (config.chalkos.router.enable && config.chalkos.router.dhcp.enable) {
    chalkos.kinds.DHCPSubnet = builtin groups.router // {
      multiple = true;
      description = "A subnet the router's DHCP server leases IPv4 addresses of on its interfaces: pools and reservations, the options its clients get and the files PXE clients boot.";
      rules = [
        (inside "pools lie" "a pool" "s.pools.all(p, c.contains(ip(p.first)) && c.contains(ip(p.last)))")
        (inside "reservations lie" "a reservation" "s.reservations.all(r, c.contains(ip(r.address)))")
        (inside "gateway lies" "a gateway" "s.gateway == null || c.contains(ip(s.gateway))")
      ];
      spec = {
        interfaces = mkOption {
          type = types.nonEmptyListOf interfaceName;
          example = [ "lan0" ];
          description = "Links the server answers on, each by its full name, without a glob: links of one VRF, each of which a Network document matching it by Name= alone gives a static address in the subnet. A link takes one subnet.";
        };
        subnet = lib.chalkos.mkOption {
          type = prefix;
          example = "192.0.2.0/24";
          description = "IPv4 range the subnet's addresses are in, of 30 bits at most.";
          rules = [
            {
              description = "A subnet is an IPv4 range of 30 bits at most, outside 0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16 and 224.0.0.0/3.";
              rule = ''cel.bind(c, cidr(self), c.ip(0).family() == "ipv4" && c.prefix() <= 30 && !["0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3"].exists(r, cidr(r).contains(c.ip(0))))'';
              messageExpression = ''"the subnet " + self + " is no IPv4 range of 30 bits at most of addresses a DHCP server leases"'';
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
          description = "Ranges the server leases addresses from to any client, inside the subnet, apart from each other and from the reservations; empty leases the reservations alone.";
        };
        gateway = mkOption {
          type = types.nullOr address;
          default = null;
          example = "192.0.2.1";
          description = "Router the clients route through, inside the subnet; null gives them none.";
        };
        dns = lib.chalkos.mkOption {
          type = types.listOf address;
          default = [ ];
          example = [ "192.0.2.1" ];
          description = "DNS servers the clients ask, IPv4 addresses.";
          rules = [
            {
              description = "The DNS servers are IPv4 addresses.";
              rule = ''self.all(a, ip(a).family() == "ipv4")'';
              messageExpression = ''"the DNS server " + self.filter(a, ip(a).family() != "ipv4")[0] + " is no IPv4 address"'';
            }
          ];
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
          description = "How long a lease lasts: a number and s, m, h or d, 136 years at most.";
        };
        reservations = lib.chalkos.mkOption {
          type = types.listOf (
            types.submodule {
              options = {
                mac = mkOption {
                  type = types.strMatching "[0-9a-f]{2}(:[0-9a-f]{2}){5}";
                  example = "52:54:00:12:34:56";
                  description = "MAC address of the client, in lower case.";
                };
                address = mkOption {
                  type = address;
                  example = "192.0.2.10";
                  description = "Address the client always gets, inside the subnet and outside its pools.";
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
          rules = [
            {
              description = "A MAC address has one reservation.";
              rule = "self.all(r, size(self.filter(x, x.mac == r.mac)) == 1)";
              messageExpression = ''"the MAC address " + self.filter(r, size(self.filter(x, x.mac == r.mac)) > 1)[0].mac + " has more than one reservation"'';
            }
            {
              description = "An address is reserved once.";
              rule = "self.all(r, size(self.filter(x, ip(x.address) == ip(r.address))) == 1)";
              messageExpression = ''"the address " + self.filter(r, size(self.filter(x, ip(x.address) == ip(r.address))) > 1)[0].address + " is reserved more than once"'';
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
                  description = "IPv4 address of the TFTP server the files are on, the DHCP header's next server; null for none, such as for files of HTTP URLs.";
                };
                bios = mkOption {
                  type = types.nullOr bootFile;
                  default = null;
                  example = "undionly.kpxe";
                  description = "File of clients with BIOS firmware, of architecture 0 in option 93.";
                };
                uefi = mkOption {
                  type = types.nullOr bootFile;
                  default = null;
                  example = "ipxe.efi";
                  description = "File of clients with x86-64 UEFI firmware, of architecture 7 or 9 in option 93.";
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
          description = "Files PXE clients boot, by firmware; boot files are served elsewhere. null serves none.";
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
          ];
        };
      };
    };
  };
}
