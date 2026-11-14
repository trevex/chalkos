# The router's DNS resolvers, on images whose chalkos.router.resolver.enable is true: Unbound,
# whose whole config the output unbound renders from each Resolver document, unbound.conf for the
# default VRF's and unbound-<vrf>.conf for a VRF's. The default VRF's runs as unbound.service, each
# VRF's as unbound@<vrf>.service, whose sockets systemd binds to the VRF's device. A node without
# a Resolver runs no Unbound; a delivery that adds or removes a resolver starts or stops it, and
# one that changes it reloads Unbound and keeps its cache.
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
  inherit (routerLib) hardened;
  kinds = routerLib.kinds ./kinds.nix;
  unbound = config.services.unbound.package;
  dir = config.chalkos.outputs.unbound.dir;
  rendered = "${dir}/unbound.conf";
  inherit (chalkosLib.internal) cel;
  inherit (chalkosLib.internal.api) apiVersions;
  # The number of the default VRF's resolvers in the config c: a running config of an image whose
  # Resolver had no vrf lacks the field.
  defaultResolvers =
    c:
    "cel.bind(r, ${
      cel.documents c apiVersions.router "Resolver"
    }, size(r.filter(n, !has(r[n].spec.vrf) || r[n].spec.vrf == \"default\")))";
  # chalkd router services unbound runs the resolvers of the VRFs: it starts the resolver of each
  # VRF with a rendered config that does not run, reloads those that run and stops those without a
  # config, after the others ran; with the argument stop, it stops all of them. systemd binds a
  # resolver's sockets to the index its VRF's device had when the resolver started, which the
  # resolver keeps in /run/unbound-<vrf>/unbound-<vrf>.ifindex. A VRF that chalkos-vrf removed and
  # networkd created anew has a new index, or no device yet, so its resolver restarts on the new
  # device. systemctl comes from the unit's PATH.
  reconcile = "${lib.getExe pkgs.chalkos.chalkd} router services unbound";
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
  options.chalkos.router = routerLib.feature "resolver" ''
    Whether router images carry Unbound, the DNS resolvers of the router's VRFs, and accept the
    kind Resolver; only with the router layer. Set it false in a variant's modules to leave
    Unbound out of its images.
  '';

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

    # The images allocate the IDs of users and groups without one statically, from 999 down, so
    # Unbound's would move the IDs of those after them whenever an image adds or drops the
    # resolver. 331 is free in NixOS's ids.nix.
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
    # A VRF's resolver: Unbound with the default VRF's hardening, whose sockets systemd binds to the
    # VRF's device, as ip vrf exec would. systemd binds them to the index the device has when the
    # unit starts, which the unit keeps for chalkos-resolvers. A device that goes and comes back
    # stops nothing: chalkos-resolvers restarts the resolver once the device is back. systemd may
    # see the old device go only after that, and BindsTo= would then stop the restarted resolver.
    systemd.services."unbound@" = {
      description = "Unbound of the VRF %I";
      after = [
        "sys-subsystem-net-devices-%i.device"
        "chalkos-config.service"
        "chalkos-firewall.service"
        "chalkos-nat64.service"
      ];
      unitConfig = {
        Requires = "sys-subsystem-net-devices-%i.device";
        ConditionPathExists = "${dir}/unbound-%I.conf";
      };
      # The default VRF's unit prepares nothing a VRF's needs.
      serviceConfig = removeAttrs config.systemd.services.unbound.serviceConfig [ "ExecStartPre" ] // {
        ExecStart = "${unbound}/bin/unbound -p -d -c ${dir}/unbound-%I.conf";
        ExecReload = "${unbound}/bin/unbound-control -c ${dir}/unbound-%I.conf reload_keep_cache";
        # systemd starts a unit whose interface it cannot find without binding its sockets: this
        # unit fails instead.
        ExecStartPre = "${routerLib.recordDevice} unbound %I";
        BindNetworkInterface = "%I";
        # unbound-control reaches it through the socket the rendered config names in
        # /run/unbound-<vrf>, which root, and so chalkd, may enter, as may the group unbound, and
        # so chalkd router services, which chalkos-resolvers runs and which reads the device's
        # index there.
        RuntimeDirectory = "unbound-%I";
        RuntimeDirectoryMode = "0750";
      };
    };
    # Runs the VRFs' resolvers of the rendered configs, after networkd, which creates the VRFs, and
    # the NAT64s, as unbound.service does.
    systemd.services.chalkos-resolvers = {
      description = "Run the resolvers of the router's VRFs";
      after = [
        "chalkos-config.service"
        "chalkos-vrf.service"
        "systemd-networkd.service"
        "chalkos-firewall.service"
        "chalkos-nat64.service"
      ];
      wantedBy = [ "multi-user.target" ];
      path = [ config.systemd.package ];
      serviceConfig = hardened // {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = reconcile;
        ExecReload = reconcile;
        ExecStop = "${reconcile} stop";
        # systemctl reaches systemd over its socket, for which root needs no capability.
        CapabilityBoundingSet = [ "" ];
        SupplementaryGroups = [ "unbound" ];
        RestrictAddressFamilies = [ "AF_UNIX" ];
      };
    };
    # chalkd asks unbound-control whether the default VRF's resolver runs, and systemctl whether
    # the VRFs' units are active.
    systemd.services.chalkd.path = [ unbound ];
  };
}
