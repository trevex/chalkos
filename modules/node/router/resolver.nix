# The router's DNS resolver: Unbound, whose whole config the output unbound renders from the
# node's Resolver document. A node without one runs no Unbound; a delivery that adds or removes
# the document starts or stops it, and one that changes it reloads Unbound and keeps its cache.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  unbound = config.services.unbound.package;
  rendered = "${config.chalkos.outputs.unbound.dir}/unbound.conf";
  cel = import ../../../lib/cel.nix { inherit lib; };
  inherit (import ../../../lib/kinds/api.nix) apiVersions;
  resolvers = c: "size(${cel.documents c apiVersions.router "Resolver"})";
  # RFC 8880's answer for ipv4only.arpa, which a DNS64 resolver gives itself; the dns64 module
  # synthesizes its AAAA records from these A records.
  ipv4only = pkgs.writeText "ipv4only.arpa.zone" ''
    ipv4only.arpa. 3600 IN SOA localhost. nobody.invalid. 1 3600 1200 604800 3600
    ipv4only.arpa. 3600 IN NS localhost.
    ipv4only.arpa. 3600 IN A 192.0.0.170
    ipv4only.arpa. 3600 IN A 192.0.0.171
  '';
in
{
  config = lib.mkIf config.chalkos.router.enable {
    services.unbound = {
      enable = true;
      # The rendered config holds the trust anchor of the image's root keys, which updates with
      # the image.
      enableRootTrustAnchor = false;
      # The node resolves its own names as before; the resolver serves the Resolver's clients.
      resolveLocalQueries = false;
      # The module's own config goes unused.
      checkconf = false;
    };
    environment.etc."unbound/unbound.conf".source = lib.mkForce rendered;

    chalkos.outputs.unbound = {
      inputs = [ "Resolver" ];
      render = "${lib.getExe chalkosPackages.chalkos-render} unbound ${pkgs.dns-root-data}/root.key ${ipv4only}";
      check = ''[ ! -e "$OUT/unbound.conf" ] || ${unbound}/bin/unbound-checkconf "$OUT/unbound.conf"'';
      examples = ../examples/router;
    };
    chalkos.effects.unbound = {
      outputs = [ "unbound" ];
      reload = [ "unbound.service" ];
    };
    # A Resolver added starts Unbound, and one removed stops it: the unit's condition no longer
    # holds once it restarts.
    chalkos.effects.unbound-start = {
      when = "${resolvers "config"} != ${resolvers "oldConfig"}";
      restart = [ "unbound.service" ];
      start = true;
    };

    systemd.services.unbound = {
      # A new resolver's port is open before it answers.
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
        "chalkos-nat64.service"
      ];
      # Without a rendered config, as in maintenance mode, the node runs no resolver.
      unitConfig.ConditionPathExists = rendered;
      serviceConfig = {
        # unbound-control reaches Unbound through the socket the rendered config names in
        # /run/unbound, which Unbound makes its user's and group's alone; the directory admits
        # them and root, so chalkd.
        ExecReload = lib.mkForce "${unbound}/bin/unbound-control reload_keep_cache";
        RuntimeDirectoryMode = "0750";
        # Unbound binds port 53 and needs no raw sockets, which only ip-transparent takes.
        AmbientCapabilities = lib.mkForce [ "CAP_NET_BIND_SERVICE" ];
        CapabilityBoundingSet = lib.mkForce [ "CAP_NET_BIND_SERVICE" ];
      };
    };
    # chalkd asks unbound-control whether the resolver runs.
    systemd.services.chalkd.path = [ unbound ];
  };
}
