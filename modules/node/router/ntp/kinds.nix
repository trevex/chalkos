# NTP on router images whose chalkos.router.ntp.enable is true: the router's chrony answers the
# clients of a TimeServer, which the output chrony-server renders as a fragment of chrony's config
# (default.nix). The router keeps its own clock with its TimeSync, as every node does.
{
  chalkosLib,
  lib,
  routerLib,
}:
let
  inherit (lib) types;
  inherit (chalkosLib.internal) builtin;
  inherit (chalkosLib.internal.api) groups;
  inherit (routerLib.types)
    prefix
    address
    unicast
    once
    listenFamily
    ;
in
{
  TimeServer = builtin groups.router // {
    description = "The router's NTP server, chrony: the addresses it answers on, whom it answers, and whether it serves its own clock when it follows no time server.";
    rules = [ (listenFamily "TimeServer") ];
    spec = {
      listen = chalkosLib.mkOption {
        type = types.nonEmptyListOf address;
        example = [ "2001:db8:1::1" ];
        description = "Addresses of the router in the default VRF on which it answers NTP, on UDP port 123; the firewall accepts NTP on them from `allow`. chrony itself answers on every address, so the firewall alone keeps the others closed.";
        rules = [
          {
            description = "The time server listens on unicast addresses.";
            rule = "self.all(a, ${unicast "a"})";
            messageExpression = ''"the address " + self.filter(a, !${unicast "a"})[0] + " is no unicast address the time server can listen on"'';
          }
        ];
        unique = once "ip" "name each address once";
      };
      allow = chalkosLib.mkOption {
        type = types.nonEmptyListOf prefix;
        example = [ "2001:db8:1::/64" ];
        description = "Address ranges of the clients the time server answers; chrony ignores others, and the firewall accepts NTP from these alone.";
        unique = once "cidr" "name each range once";
      };
      localStratum = chalkosLib.mkOption {
        type = types.nullOr (types.ints.between 1 15);
        default = null;
        example = 10;
        description = "Stratum at which the router serves its own clock while it follows no time server, as chrony's `local stratum <n> orphan`: five minutes after chrony starts without a source, or once the estimated error of the source it followed passes a second, its clients take the router's clock, however far from the real time it is. Null answers as unsynchronised then, which clients refuse. Give a stratum above that of every server on the network, such as 10.";
      };
    };
  };
}
