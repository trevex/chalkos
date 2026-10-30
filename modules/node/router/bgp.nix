# BGP on router images: FRR's zebra, bgpd, bfdd and staticd, whose whole config the output frr
# renders from the node's BGP documents. frr.service fills the peers' passwords in from its
# credentials into /run/frr/frr.conf before it starts and on each reload, which applies the
# changes with frr-reload.py and keeps the sessions they leave alone.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  render = lib.getExe chalkosPackages.chalkos-render;
  frr = pkgs.frr;
  rendered = "${config.chalkos.outputs.frr.dir}/frr.conf";
  # vtysh reads vtysh.conf from its config directory; the check's own holds no more, so vtysh
  # checks the rendered file alone.
  checkDir = pkgs.writeTextDir "vtysh.conf" "service integrated-vtysh-config\n";
  # The config of a node without a rendered one, as in maintenance mode: FRR runs no BGP.
  fallback = pkgs.runCommand "frr-fallback.conf" { } ''
    mkdir out
    echo '{"node": {"name": ""}, "documents": {}}' | OUT=$PWD/out ${render} frr
    cp out/frr.conf $out
  '';
  # Writes /run/frr/frr.conf, readable by root alone, through a new file that replaces the old
  # one: the rendered config, or the fallback, with the placeholder @secret_<secret>.<key>@ of
  # each peer group's password line replaced by the unit's credential of that name. A
  # placeholder elsewhere stays as written. FRR takes a password of printable characters without
  # white space, 80 at most; another, or a credential the unit lacks, fails the unit and leaves
  # the file as it was.
  credentials = pkgs.writeShellApplication {
    name = "frr-credentials";
    text = ''
      # Bytes, so that the pattern and the length refuse what is no printable ASCII.
      export LC_ALL=C
      src=${rendered}
      [[ -e $src ]] || src=${fallback}
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
  config = lib.mkIf config.chalkos.router.enable {
    services.frr = {
      bgpd.enable = true;
      bfdd.enable = true;
      # /etc/frr/frr.conf links to the filled config, which vtysh -b loads once the daemons run.
      configFile = "/run/frr/frr.conf";
    };

    chalkos.outputs.frr = {
      inputs = [
        "BGP"
        "BGPPeer"
        "PrefixList"
        "CommunityList"
        "RoutingPolicy"
        "Network"
        "VRF"
      ];
      render = "${render} frr";
      # vtysh parses the config as the daemons' commands take it, without the daemons.
      check = "${frr}/bin/vtysh --dryrun --config_dir ${checkDir} -f \"$OUT/frr.conf\"";
      examples = ../examples/router;
    };
    # A reload of a failed frr.service restarts it, which fills the config in as a reload does.
    chalkos.effects.frr = {
      outputs = [ "frr" ];
      reload = [ "frr.service" ];
    };

    # chalkd loads the values the peers' password fields name as frr.service's credentials.
    systemd.services.frr = {
      chalkos.kinds = [ "BGPPeer" ];
      # A new peer's ports are open, and networkd has read the VRF's units, before FRR reloads: a
      # delivery reloads these units with one systemctl call, whose job of each unit takes this
      # order.
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
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
        # restart. frr-reload.py logs to the journal, and reads the filled config through
        # /etc/frr/frr.conf: given another file, it would write the running config over that one.
        RefreshOnReload = "credentials";
        ExecReload = lib.mkForce [
          (lib.getExe credentials)
          "${frr}/libexec/frr/frr-reload.py --reload --stdout --bindir ${frr}/bin --confdir /etc/frr --rundir /run/frr /etc/frr/frr.conf"
        ];
      };
    };
    # chalkd reads FRR's state with vtysh for the node's status and its routes.
    systemd.services.chalkd.path = [ frr ];

    # zebra installs BGP's routes and next hops, which networkd would remove as foreign ones each
    # time it configures a link.
    systemd.network.config.networkConfig = {
      ManageForeignRoutes = false;
      ManageForeignNextHops = false;
    };
  };
}
