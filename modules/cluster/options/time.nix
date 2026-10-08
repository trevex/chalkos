# The time servers nodes keep their clocks with. Certificates are checked against the clock, so a
# node whose clock is off can reach no other node.
{ lib, ... }:
{
  options.chalkos.time = {
    servers = lib.mkOption {
      type = lib.types.listOf (import ../time.nix { inherit lib; });
      default = map (n: { host = "ptbtime${toString n}.ptb.de"; }) [
        1
        2
        3
      ];
      defaultText = lib.literalExpression ''[ { host = "ptbtime1.ptb.de"; } { host = "ptbtime2.ptb.de"; } { host = "ptbtime3.ptb.de"; } ]'';
      description = ''
        Time servers every node synchronises its clock with, authenticated with NTS unless
        `nts = false`. A node's `chalkos.nodes.<name>.time.servers` replaces them.
      '';
    };
    dhcpServers = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Also synchronise with the time servers the network announces through DHCP, which are not
        authenticated.
      '';
    };
  };
}
