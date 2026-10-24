# The time servers the node synchronises its clock with.
{ lib, ... }:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin groups;
  timeServer = types.submodule {
    options = {
      host = mkOption {
        type = types.strMatching "[A-Za-z0-9][A-Za-z0-9.:-]*";
        description = "Host name or address of the time server.";
      };
      nts = mkOption {
        type = types.bool;
        default = true;
        description = "Whether the node authenticates the server with Network Time Security (NTS).";
      };
      port = mkOption {
        type = types.nullOr types.port;
        default = null;
        description = "UDP port of the server's NTP; null is NTP's port, 123.";
      };
    };
  };
in
{
  chalkos.kinds = {
    TimeSync = builtin groups.time // {
      description = "The time servers the node synchronises its clock with.";
      spec = {
        servers = mkOption {
          type = types.listOf timeServer;
          default = map (n: { host = "ptbtime${toString n}.ptb.de"; }) [
            1
            2
            3
          ];
          description = "Time servers the node synchronises its clock with, authenticated with NTS unless `nts = false`.";
        };
        dhcp = mkOption {
          type = types.bool;
          default = false;
          description = "Whether the node also synchronises with the time servers the network announces through DHCP, which are not authenticated.";
        };
      };
    };
  };
}
