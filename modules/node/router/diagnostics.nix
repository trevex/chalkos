# The diagnostics router images add to every image's: the routers on the path to an address.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  config = lib.mkIf config.chalkos.router.enable {
    # traceroute's default UDP probes need no privileges; one probe a hop with a wait of two
    # seconds keeps a path of 30 hops within the timeout.
    chalkos.diagnostics.traceroute = {
      description = "The routers on the path to an address, as traceroute -n prints them: one probe a hop, 30 hops at most.";
      command = [
        "${pkgs.traceroute}/bin/traceroute"
        "-n"
        "-m"
        "30"
        "-q"
        "1"
        "-w"
        "2"
        "{address}"
      ];
      parameters.address = {
        type = "address";
        description = "IPv4 or IPv6 address.";
      };
      # It sends traffic.
      role = "operator";
      timeout = "90s";
    };
  };
}
