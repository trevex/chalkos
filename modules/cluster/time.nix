# A time server, as the cluster's and each node's time options list them.
{ lib }:
lib.types.submodule {
  options = {
    host = lib.mkOption {
      type = lib.types.strMatching "[A-Za-z0-9][A-Za-z0-9.:-]*";
      example = "ptbtime1.ptb.de";
      description = "Host name or address of the time server.";
    };
    nts = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Authenticate the server with Network Time Security (NTS).";
    };
    port = lib.mkOption {
      type = lib.types.nullOr lib.types.port;
      default = null;
      description = "UDP port of the server's NTP; null is NTP's port, 123.";
    };
  };
}
