# FRR on images whose layer runs BGP: zebra, bgpd, bfdd and staticd, whose whole config a layer's
# output renders as frr.conf. frr.service fills the peers' passwords in from its credentials into
# /run/frr/frr.conf before it starts and on each reload, which applies the changes with
# chalkos-frr-reload and keeps the sessions they leave alone. FRR is chalkos's build, without Python
# in its closure. A layer's BGP feature turns it on and names the rendered config and the config of
# a node without one.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.frr;
  inherit (pkgs) frr;
  # vtysh reads vtysh.conf from its config directory; the check's own holds no more, so vtysh
  # checks the rendered file alone.
  checkDir = pkgs.writeTextDir "vtysh.conf" "service integrated-vtysh-config\n";
  # Writes /run/frr/frr.conf, readable by root alone, through a new file that replaces the old: the
  # rendered config, or the fallback, with the placeholder @secret_<secret>.<key>@ of each peer
  # group's password line replaced by the unit's credential of that name; one elsewhere stays.
  # FRR takes 1 to 80 printable characters without white space; another password, or a missing
  # credential, fails the unit and leaves the file as it was.
  credentials = pkgs.writeShellApplication {
    name = "frr-credentials";
    text = ''
      # Bytes, so that the pattern and the length refuse what is no printable ASCII.
      export LC_ALL=C
      src=${cfg.rendered}
      [[ -e $src ]] || src=${cfg.fallback}
      umask 077
      tmp=$(mktemp /run/frr/frr.conf.XXXXXX)
      trap 'rm -f "$tmp"' EXIT
      while IFS= read -r line || [[ -n $line ]]; do
        if [[ $line =~ ^(\ neighbor\ peer-[a-z0-9.-]+\ password\ )@(secret_[A-Za-z0-9._-]+)@$ ]]; then
          head=''${BASH_REMATCH[1]}
          name=''${BASH_REMATCH[2]}
          file=''${CREDENTIALS_DIRECTORY:?frr.service has no credentials}/$name
          if [[ ! -f $file ]]; then
            echo "frr.conf names the credential $name, which frr.service does not have" >&2
            exit 1
          fi
          # The x keeps a trailing line break, and the size a NUL, which the shell drops.
          value=$(cat "$file" && echo x)
          value=''${value%x}
          if [[ ! $value =~ ^[!-~]{1,80}$ || ''${#value} != "$(stat -c %s "$file")" ]]; then
            echo "$name: FRR takes a password of 1 to 80 printable characters without white space or line breaks" >&2
            exit 1
          fi
          line=$head$value
        fi
        printf '%s\n' "$line"
      done <"$src" >"$tmp"
      mv "$tmp" /run/frr/frr.conf
    '';
  };
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
    check = lib.mkOption {
      type = lib.types.str;
      internal = true;
      readOnly = true;
      # vtysh parses the config as the daemons' commands take it, without the daemons.
      default = "${frr}/bin/vtysh --dryrun --config_dir ${checkDir} -f \"$OUT/frr.conf\"";
      description = "The check of an output that renders frr.conf.";
    };
  };

  config = lib.mkIf cfg.enable {
    # NixOS's FRR module and chalkd run pkgs.frr.
    nixpkgs.overlays = [ (final: _: { inherit (final.chalkos) frr; }) ];

    # The images allocate unpinned IDs from 999 down, so a feature that adds or drops a user would
    # move the IDs after it; 329 and 330 are free in NixOS's ids.nix.
    users.users.frr.uid = 329;
    users.groups.frr.gid = 329;
    users.groups.frrvty.gid = 330;

    services.frr = {
      bgpd.enable = true;
      bfdd.enable = true;
      # /etc/frr/frr.conf links to the filled config, which vtysh -b loads once the daemons run.
      configFile = "/run/frr/frr.conf";
    };

    systemd.services.frr = {
      # networkd has read the links' units before FRR reloads: a delivery reloads these units with
      # one systemctl call, whose job of each unit takes this order.
      after = [
        "chalkos-config.service"
        "systemd-networkd.service"
      ];
      # FRR announces the node's addresses.
      before = [ "chalkos-node-addresses.target" ];
      wantedBy = [ "chalkos-node-addresses.target" ];
      # chalkd writes the unit's credentials once the node has config; in maintenance mode, which
      # loads none, the node runs no FRR.
      unitConfig.ConditionPathIsDirectory = "/run/chalkos/credentials/frr";
      serviceConfig = {
        ExecStartPre = lib.getExe credentials;
        # A reload loads the credentials again, so a rotated password reaches FRR without a
        # restart. chalkos-frr-reload logs the commands it runs, without the passwords, to the
        # journal; a config it cannot apply fails the reload, and a restart loads it whole.
        RefreshOnReload = "credentials";
        ExecReload = lib.mkForce [
          (lib.getExe credentials)
          "${lib.getExe pkgs.chalkos.chalkos-frr-reload} -vtysh ${frr}/bin/vtysh /run/frr/frr.conf"
        ];
      };
    };

    # zebra installs BGP's routes and next hops, which networkd would remove as foreign ones each
    # time it configures a link.
    systemd.network.config.networkConfig = {
      ManageForeignRoutes = false;
      ManageForeignNextHops = false;
    };
  };
}
