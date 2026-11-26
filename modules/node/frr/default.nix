# FRR on images whose layer runs BGP: mgmtd, zebra, staticd, bfdd and bgpd, whose whole config a
# layer's output renders as frr.conf. frr.service fills the peers' passwords in from its credentials
# into /run/frr/frr.conf before the daemons start, and again on each reload, which applies the
# changes with chalkos-frr-reload and keeps the sessions they leave alone. Each daemon runs as a
# unit of its own, frr-<daemon>.service, which loads the daemon's part of the config once it runs
# and which systemd restarts when it fails, as FRR's watchfrr would: a failed zebra stops its
# clients, which frr.service starts again once zebra runs. No unit runs a shell. FRR is chalkos's
# build, without Python, SNMP, Lua or watchfrr in its closure. A layer's BGP feature turns it on
# and names the rendered config, the config of a node without one and, if FRR runs only on nodes
# whose config holds BGP, the marker that tells them.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.frr;
  inherit (pkgs.chalkos) frr;
  inherit (chalkosLib.sandbox) hardened;
  # vtysh reads vtysh.conf from its config directory; the check's own holds no more, so vtysh
  # checks the rendered file alone.
  checkDir = pkgs.writeTextDir "vtysh.conf" "service integrated-vtysh-config\n";
  # Writes /run/frr/frr.conf, readable by root alone, through a new file that replaces the old: the
  # rendered config, or the fallback, with the placeholder @secret_<secret>.<key>@ of each peer
  # group's password line replaced by the unit's credential of that name; one elsewhere stays.
  # FRR takes 1 to 80 printable characters without white space; another password, or a missing
  # credential, fails the unit and leaves the file as it was.
  fill = "${lib.getExe pkgs.chalkos.chalkd} frr-credentials ${cfg.rendered} ${cfg.fallback}";
  # chalkd writes frr.service's credentials once the node has config; in maintenance mode, which
  # loads none, the node runs no FRR. A layer may name a marker besides, which its output writes
  # when the node runs BGP.
  condition = {
    ConditionPathIsDirectory = "/run/chalkos/credentials/frr";
  }
  // lib.optionalAttrs (cfg.marker != null) { ConditionPathExists = cfg.marker; };
  # The daemons, with those each starts after, in the order FRR's scripts start them, their
  # options and the capabilities each keeps once it runs as frr, which it asks for by name and
  # without which it does not start: the VTY's TCP port listens on the loopback address alone, as
  # NixOS's FRR module has it, and zebra's netlink socket takes 90 MB, enough for the routes of a
  # full table. Every daemon but mgmtd, which holds the config of staticd and zebra, is a client
  # of zebra. bfdd sends its echo packets through a packet socket.
  daemons = {
    mgmtd = {
      after = [ ];
      options = "-A 127.0.0.1";
      capabilities = [
        "CAP_NET_BIND_SERVICE"
        "CAP_NET_RAW"
        "CAP_NET_ADMIN"
        "CAP_SYS_ADMIN"
      ];
    };
    zebra = {
      after = [ "mgmtd" ];
      options = "-A 127.0.0.1 -s 90000000";
      capabilities = [
        "CAP_NET_ADMIN"
        "CAP_SYS_ADMIN"
        "CAP_NET_RAW"
      ];
    };
    bfdd = {
      after = [ "zebra" ];
      options = "-A 127.0.0.1";
      capabilities = [
        "CAP_NET_BIND_SERVICE"
        "CAP_SYS_ADMIN"
        "CAP_NET_RAW"
      ];
      families = [ "AF_PACKET" ];
    };
    bgpd = {
      after = [
        "zebra"
        "bfdd"
      ];
      options = "-A 127.0.0.1";
      capabilities = [
        "CAP_NET_BIND_SERVICE"
        "CAP_NET_RAW"
        "CAP_NET_ADMIN"
        "CAP_SYS_ADMIN"
      ];
    };
    staticd = {
      after = [
        "mgmtd"
        "zebra"
      ];
      options = "-A 127.0.0.1";
      capabilities = [ ];
    };
  };
  unit = name: "frr-${name}.service";
  units = map unit (lib.attrNames daemons);
in
{
  imports = [ ./diagnostics.nix ];

  options.chalkos.frr = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      internal = true;
      description = "Whether the image runs FRR, which a layer's BGP feature sets.";
    };
    rendered = lib.mkOption {
      type = lib.types.str;
      internal = true;
      description = "Path of the rendered frr.conf, an output's file, whose password lines of peer groups peer-<name> hold the placeholders @secret_<secret>.<key>@ that frr.service fills in.";
    };
    fallback = lib.mkOption {
      type = lib.types.path;
      internal = true;
      description = "frr.conf of a node without a rendered one, as in maintenance mode, which runs no BGP.";
    };
    marker = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      internal = true;
      description = "Path of a file, an output's, without which FRR's units do not start, so a node whose config holds no BGP runs no FRR; null to start FRR on every node with config.";
    };
    check = lib.mkOption {
      type = lib.types.str;
      internal = true;
      readOnly = true;
      # vtysh parses the config as the daemons' commands take it, without the daemons.
      default = "${lib.getExe pkgs.chalkos.chalkos-render} check run ${frr}/bin/vtysh --dryrun --config_dir ${checkDir} -f $OUT/frr.conf";
      description = "The check of an output that renders frr.conf.";
    };
  };

  config = lib.mkIf cfg.enable (
    lib.mkMerge [
      {
        # The images allocate unpinned IDs from 999 down, so a feature that adds or drops a user
        # would move the IDs after it; 329 and 330 are free in NixOS's ids.nix. The daemons start
        # as root and run as frr; members of frrvty may use their sockets, as vtysh does.
        users.users.frr = {
          uid = 329;
          group = "frr";
          isSystemUser = true;
          description = "FRR's daemons";
        };
        users.groups.frr.gid = 329;
        users.groups.frrvty = {
          gid = 330;
          members = [ "frr" ];
        };

        # vtysh, which loads the config into the daemons, reads the config of all daemons from
        # /etc/frr/frr.conf, a link to the filled config. The daemons find it there and read no
        # config of their own when they start.
        environment.etc = {
          "frr/frr.conf".source = "/run/frr/frr.conf";
          "frr/vtysh.conf".text = "service integrated-vtysh-config\n";
        };
        environment.systemPackages = [ frr ];
        # The daemons' sockets and PID files, and mgmtd's record of the commits of its config.
        systemd.tmpfiles.rules = [
          "d /run/frr 0755 frr frr -"
          "d /var/lib/frr 0750 frr frr -"
        ];

        # chalkd reads the speaker's sessions for the router and Kubernetes layers.
        systemd.services.chalkd.path = [ frr ];

        # frr.service fills the config in, and the daemons start after it: a stop or restart of
        # frr.service stops or restarts them all. It upholds them, so it starts the clients of zebra
        # again once zebra runs after a failure.
        systemd.services.frr = {
          description = "FRR's config and daemons";
          # At boot, networkd reads the links' units before FRR starts.
          after = [
            "chalkos-config.service"
            "systemd-networkd.service"
          ];
          wants = units;
          upholds = units;
          # FRR runs at boot, also on images where nothing wants chalkos-node-addresses.target,
          # and announces the node's addresses.
          before = [ "chalkos-node-addresses.target" ];
          wantedBy = [
            "multi-user.target"
            "chalkos-node-addresses.target"
          ];
          unitConfig = condition;
          # The fill and chalkos-frr-reload, which runs vtysh, run as root and write the config
          # and vtysh's script into /run/frr, which is frr's, and vtysh talks to the daemons over
          # their sockets, which are frrvty's: both take CAP_DAC_OVERRIDE.
          serviceConfig = hardened // {
            CapabilityBoundingSet = [ "CAP_DAC_OVERRIDE" ];
            ReadWritePaths = [ "/run/frr" ];
            Type = "oneshot";
            RemainAfterExit = true;
            ExecStart = fill;
            # A reload loads the credentials again, so a rotated password reaches FRR without a
            # restart. chalkos-frr-reload logs the commands it runs, without the passwords, to the
            # journal; a config it cannot apply fails the reload, and a restart loads it whole.
            RefreshOnReload = "credentials";
            ExecReload = [
              fill
              "${lib.getExe pkgs.chalkos.chalkos-frr-reload} -vtysh ${frr}/bin/vtysh /run/frr/frr.conf"
            ];
          };
        };
      }
      {
        # Each daemon forks once its VTY listens, as FRR's scripts start it, and vtysh then loads
        # its commands of the config into it: at boot, and after each restart of the daemon alone,
        # which starts without config. A config vtysh cannot load fails the daemon. A daemon that
        # fails is restarted; zebra's clients stop with it and start again after it.
        systemd.services = lib.mapAttrs' (
          name: daemon:
          lib.nameValuePair "frr-${name}" {
            description = "FRR's ${name}";
            requires = [ "frr.service" ];
            partOf = [ "frr.service" ];
            bindsTo = lib.optional (name != "mgmtd" && name != "zebra") (unit "zebra");
            after = [ "frr.service" ] ++ map unit daemon.after;
            before = [ "chalkos-node-addresses.target" ];
            unitConfig = condition;
            # A daemon starts as root, hands its crash log directory below its private /var/tmp to
            # frr, which takes CAP_CHOWN, switches to frr with the capabilities it names and drops
            # the rest. It writes its socket and PID file to /run/frr and mgmtd its commits to
            # /var/lib/frr. vtysh -b loads the config as root, which reads the filled config as its
            # owner and reaches the daemon's socket as a member of frrvty.
            serviceConfig = hardened // {
              CapabilityBoundingSet = [
                "CAP_CHOWN"
                "CAP_SETUID"
                "CAP_SETGID"
              ]
              ++ daemon.capabilities;
              RestrictAddressFamilies = [
                "AF_UNIX"
                "AF_NETLINK"
                "AF_INET"
                "AF_INET6"
              ]
              ++ daemon.families or [ ];
              ReadWritePaths = [
                "/run/frr"
                "/var/lib/frr"
              ];
              SupplementaryGroups = [ "frrvty" ];
              Type = "forking";
              PIDFile = "/run/frr/${name}.pid";
              ExecStart = "${frr}/libexec/frr/${name} -d ${daemon.options}";
              ExecStartPost = "${frr}/bin/vtysh -b -d ${name}";
              Restart = "on-failure";
              RestartSec = 1;
              Nice = -5;
            };
          }
        ) daemons;

        # zebra installs BGP's routes and next hops, which networkd would remove as foreign ones
        # each time it configures a link.
        systemd.network.config.networkConfig = {
          ManageForeignRoutes = false;
          ManageForeignNextHops = false;
        };
      }
    ]
  );
}
