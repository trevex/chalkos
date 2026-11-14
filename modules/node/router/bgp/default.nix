# BGP on router images whose chalkos.router.bgp.enable is true: FRR's zebra, bgpd, bfdd and
# staticd, whose whole config the output frr renders from the node's BGP documents. frr.service
# fills the peers' passwords in from its credentials into /run/frr/frr.conf before it starts and
# on each reload, which applies the changes with frr-reload.py and keeps the sessions they leave
# alone.
{
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.bgp;
  kinds = routerLib.kinds ./kinds.nix;
  render = lib.getExe pkgs.chalkos.chalkos-render;
  inherit (pkgs) frr;
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
  imports = [ ./diagnostics.nix ];

  options.chalkos.router.bgp.enable = routerLib.feature ''
    Whether router images carry FRR, which runs BGP and BFD, and accept the kinds BGP, BGPPeer,
    PrefixList, CommunityList and RoutingPolicy; only with the router layer. Set it false in a
    variant's modules to leave FRR and the Python of frr-reload.py out of its images.
  '';

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

    # The images allocate the IDs of users and groups without one statically, from 999 down, so
    # FRR's would move the IDs of those after them whenever an image adds or drops BGP. 329 and
    # 330 are free in NixOS's ids.nix, as kea's 328 is.
    users.users.frr.uid = 329;
    users.groups.frr.gid = 329;
    users.groups.frrvty.gid = 330;

    services.frr = {
      bgpd.enable = true;
      bfdd.enable = true;
      # /etc/frr/frr.conf links to the filled config, which vtysh -b loads once the daemons run.
      configFile = "/run/frr/frr.conf";
    };

    chalkos.outputs.frr = {
      # BGP's kinds, and the links of each VRF, the WireGuards' among them.
      inputs =
        lib.attrNames kinds
        ++ [
          "Network"
          "VRF"
        ]
        ++ routerLib.declared [ "WireGuard" ];
      render = "${render} frr";
      # vtysh parses the config as the daemons' commands take it, without the daemons.
      check = "${frr}/bin/vtysh --dryrun --config_dir ${checkDir} -f \"$OUT/frr.conf\"";
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
  };
}
