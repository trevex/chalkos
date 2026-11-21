# DHCP's diagnostics: the DHCP servers' leases, which Kea gives on its control sockets to the
# group kea.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  config = lib.mkIf (config.chalkos.router.enable && config.chalkos.router.dhcp.enable) {
    chalkos.diagnostics = {
      dhcp-leases = {
        description = "The leases of the DHCPv4 and DHCPv6 servers of every VRF, or of one subnet or MAC address: each lease's VRF, subnet, address or delegated prefix, MAC address, host name and end, as Kea's lease commands report them.";
        # chalkd asks each DHCP server on its control socket for its subnets and its leases, and
        # prints the leases of the subnet and MAC address given, or all, a line each. A server that
        # does not answer a question with JSON within 2 seconds, or whose answers hold no subnets
        # and leases, is named on standard error, and the others still listed. Servers still
        # unasked after 24 seconds are named as well, so the listing ends within the timeout.
        command = [
          (lib.getExe pkgs.chalkos.chalkd)
          "router"
          "dhcp-leases"
          "--subnet={subnet}"
          "--mac={mac}"
        ];
        parameters = {
          # A DHCPSubnet document's name, as the kinds take it.
          subnet = {
            type = "pattern";
            regex = "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$";
            optional = true;
            description = "Name of the DHCPSubnet document whose leases to list.";
          };
          mac = {
            type = "pattern";
            regex = "^[0-9a-f]{2}(:[0-9a-f]{2}){5}$";
            optional = true;
            description = "MAC address, in lower case, whose leases to list.";
          };
        };
        # The leases show which machines are on the networks.
        role = "operator";
        timeout = "30s";
        # Kea's control sockets in /run/kea admit the group kea.
        groups = [ "kea" ];
      };
    };
  };
}
