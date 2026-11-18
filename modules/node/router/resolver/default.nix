# The router's DNS resolvers: Unbound, whose whole config the output unbound renders from the
# Resolver documents: unbound.conf for the default VRF (unbound.service), unbound-<vrf>.conf for a
# VRF (unbound@<vrf>.service). A node without a Resolver runs no Unbound; adding or removing a
# resolver starts or stops it, changing one reloads Unbound, which keeps its cache.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.resolver;
  kinds = routerLib.kinds ./kinds.nix;
  unbound = config.services.unbound.package;
  dir = config.chalkos.outputs.unbound.dir;
  rendered = "${dir}/unbound.conf";
  networkd = config.chalkos.outputs.networkd.dir;
  waitForResolverVRFs = pkgs.writeShellApplication {
    name = "chalkos-resolver-vrfs-ready";
    runtimeInputs = [
      pkgs.gawk
      pkgs.iproute2
      pkgs.jq
    ];
    text = ''
      shopt -s nullglob
      configs=("${dir}"/unbound-*.conf)
      if ((''${#configs[@]} == 0)); then
        exit 0
      fi
      links=$(ip -d -j link show)
      for file in "''${configs[@]}"; do
        vrf=''${file##*/unbound-}
        vrf=''${vrf%.conf}
        table=$(awk -F= '$1 == "Table" {print $2}' "${networkd}/00-vrf-$vrf.netdev")
        if [[ -z $table ]]; then
          exit 1
        fi
        if jq -e --arg vrf "$vrf" --arg table "$table" \
          'any(.[]; .ifname == $vrf and .linkinfo.info_kind == "vrf"
            and (.linkinfo.info_data.table | tostring) == $table)' <<<"$links" >/dev/null; then
          continue
        else
          result=$?
          if ((result == 1)); then
            exit 75
          fi
          exit "$result"
        fi
      done
    '';
  };
  inherit (chalkosLib.internal) cel;
  inherit (chalkosLib.internal.api) apiVersions;
  # The number of the default VRF's resolvers in the config c.
  defaultResolvers =
    c:
    "cel.bind(r, ${
      cel.documents c apiVersions.router "Resolver"
    }, size(r.filter(n, r[n].spec.vrf == \"default\")))";
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
  options.chalkos.router = routerLib.feature "resolver" {
    carries = "Unbound, the DNS resolvers of the router's VRFs,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

    # Pinned IDs, see router/default.nix; 331 is free in NixOS's ids.nix.
    users.users.unbound.uid = 331;
    users.groups.unbound.gid = 331;

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
      # The VRFs and the Networks tell which VRF holds each listen address.
      inputs = lib.attrNames kinds ++ [
        "VRF"
        "Network"
      ];
      render = "${lib.getExe pkgs.chalkos.chalkos-render} unbound ${pkgs.dns-root-data}/root.key ${ipv4only}";
      check = ''for f in "$OUT"/*.conf; do [ ! -e "$f" ] || ${unbound}/bin/unbound-checkconf "$f" || exit; done'';
    };
    chalkos.effects.unbound = {
      outputs = [ "unbound" ];
      reload = [ "unbound.service" ];
    };
    # The default VRF's resolver added starts Unbound, and one removed stops it: the unit's
    # condition no longer holds once it restarts.
    chalkos.effects.unbound-start = {
      when = "${defaultResolvers "config"} != ${defaultResolvers "oldConfig"}";
      restart = [ "unbound.service" ];
      start = true;
    };
    # A VRF's resolver added, changed or removed, and a VRF networkd creates anew, which stopped the
    # resolver bound to it.
    chalkos.effects.resolvers = {
      outputs = [
        "unbound"
        "networkd"
      ];
      reload = [ "chalkos-resolvers.service" ];
      after = [
        "vrf"
        "networkd"
        "router-nftables"
      ]
      ++ lib.optional config.chalkos.router.nat64.enable "jool";
      waitFor = {
        description = "VRFs used by the rendered resolvers to exist with their configured routing tables";
        argv = [ (lib.getExe waitForResolverVRFs) ];
      };
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
    # A VRF's resolver: Unbound with the default VRF's sandbox. The default VRF's ExecStartPre
    # prepares nothing a VRF's needs.
    systemd.services."unbound@" = routerLib.vrfService {
      name = "unbound";
      description = "Unbound of the VRF %I";
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
        "chalkos-nat64.service"
      ];
      conf = "${dir}/unbound-%I.conf";
      serviceConfig = config.systemd.services.unbound.serviceConfig // {
        ExecStart = "${unbound}/bin/unbound -p -d -c ${dir}/unbound-%I.conf";
        ExecReload = "${unbound}/bin/unbound-control -c ${dir}/unbound-%I.conf reload_keep_cache";
        # unbound-control reaches it through the socket the rendered config names in
        # /run/unbound-<vrf>, which root (chalkd) and the group unbound (chalkos-resolvers, which
        # reads the device's index there) may enter.
        RuntimeDirectory = "unbound-%I";
        RuntimeDirectoryMode = "0750";
      };
    };
    # After the NAT64s too, as unbound.service.
    systemd.services.chalkos-resolvers = routerLib.reconciler {
      service = "unbound";
      description = "Run the resolvers of the router's VRFs";
      after = [ "chalkos-nat64.service" ];
      groups = [ "unbound" ];
    };
    # chalkd asks unbound-control whether the default VRF's resolver runs, and systemctl whether
    # the VRFs' units are active.
    systemd.services.chalkd.path = [ unbound ];
  };
}
