# NAT64's diagnostics: a NAT64 instance's sessions, which Jool shows to CAP_NET_ADMIN in the
# instance's namespace alone.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  config = lib.mkIf (config.chalkos.router.enable && config.chalkos.router.nat64.enable) {
    chalkos.diagnostics = {
      nat64-sessions = {
        description = "A NAT64 instance's sessions, as jool session display prints them: each translated TCP, UDP and ICMP connection's IPv6 and IPv4 addresses and ports, and when it expires.";
        command = [
          "${pkgs.bash}/bin/bash"
          "-c"
          ''
            for protocol in --tcp --udp --icmp; do
              ${pkgs.jool-cli}/bin/jool -i "$1" session display --numeric "$protocol" || exit
            done
          ''
          "nat64-sessions"
          "{instance}"
        ];
        # A NAT64 document's name of 11 characters at most, as the kinds take it, in ShortName's
        # alphabet of pkg/router.
        parameters.instance = {
          type = "pattern";
          regex = "^[a-z0-9]([-a-z0-9.]{0,9}[a-z0-9])?$";
          description = "Name of the NAT64 document, which names its instance and the instance's network namespace.";
        };
        # Jool answers about the instances of the namespace it runs in.
        networkNamespace = "nat64-{instance}";
        # The sessions show who connects where.
        role = "operator";
        timeout = "30s";
        capabilities = [ "CAP_NET_ADMIN" ];
      };
    };
  };
}
