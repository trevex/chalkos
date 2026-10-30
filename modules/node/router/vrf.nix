# VRFs on router images: the output networkd renders the VRF documents beside networkd's own, with
# chalkos-render router-networkd, as a VRF device's units and VRF= on its members' Networks. networkd
# creates a VRF on reload but neither changes an existing VRF's table nor removes a VRF whose unit
# went, so chalkos-vrf removes those and reloads networkd, which then creates them anew with their
# members.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  networkd = config.chalkos.outputs.networkd.dir;
  # Removes each kernel VRF that no .netdev of Kind=vrf declares with its table, and reloads
  # a running networkd when it removed one, which creates the VRF anew with its members. Reloading
  # networkd itself leaves no VRF removed whatever order a delivery's reloads take; a networkd that
  # does not run, as at boot, reads every unit when it starts. systemctl and networkctl come from
  # the unit's PATH.
  apply = pkgs.writeShellApplication {
    name = "chalkos-vrf";
    runtimeInputs = [
      pkgs.iproute2
      pkgs.jq
      pkgs.gawk
      pkgs.coreutils
    ];
    text = ''
      declare -A want=() seen=()
      shopt -s nullglob
      # networkd takes a unit of a name from the first of these directories that holds one; an
      # empty unit or a link to /dev/null masks those of the later ones.
      for file in /etc/systemd/network/*.netdev ${networkd}/*.netdev \
        /usr/local/lib/systemd/network/*.netdev /usr/lib/systemd/network/*.netdev; do
        base=''${file##*/}
        if [[ -n ''${seen[$base]-} ]]; then
          continue
        fi
        seen[$base]=1
        while IFS=$'\t' read -r name table; do
          want[$name]=$table
        done < <(awk -F= '
          function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
          /^[ \t]*[#;]/ { next }
          /^[ \t]*\[/ { section = trim($0); next }
          { key = trim($1); value = trim(substr($0, index($0, "=") + 1)) }
          section == "[NetDev]" && key == "Kind" { kind = value }
          section == "[NetDev]" && key == "Name" { name = value }
          section == "[VRF]" && key == "Table" { table = value }
          END { if (kind == "vrf" && name != "") printf "%s\t%s\n", name, table }' "$file")
      done
      vrfs=$(ip -d -j link show type vrf | jq -r '.[] | "\(.ifname) \(.linkinfo.info_data.table)"')
      failed=()
      removed=false
      while read -r name table; do
        if [[ -n $name && ''${want[$name]-} != "$table" ]]; then
          if ip link delete "$name"; then
            removed=true
          else
            failed+=("$name")
          fi
        fi
      done <<<"$vrfs"
      if $removed && systemctl is-active --quiet systemd-networkd.service && ! networkctl reload; then
        echo 'chalkos-vrf: failed to reload networkd, which creates the removed VRFs anew' >&2
        exit 1
      fi
      if ((''${#failed[@]} > 0)); then
        printf 'chalkos-vrf: failed to remove the VRF %s\n' "''${failed[@]}" >&2
        exit 1
      fi
    '';
  };
in
{
  config = lib.mkIf config.chalkos.router.enable {
    # The output is networkd's, whose directory networkd reads; the router layer renders it with
    # the VRF documents.
    chalkos.outputs.networkd = {
      inputs = [ "VRF" ];
      render = "${lib.getExe chalkosPackages.chalkos-render} router-networkd ${config.system.build.chalkosNetworkdSchemas}";
    };
    chalkos.effects.vrf = {
      outputs = [ "networkd" ];
      reload = [ "chalkos-vrf.service" ];
    };

    # A delivery reloads chalkos-vrf and networkd with one systemctl call, whose job of each unit
    # takes this order.
    systemd.services.chalkos-vrf = {
      description = "Remove the VRFs the config no longer declares";
      after = [ "chalkos-config.service" ];
      before = [ "systemd-networkd.service" ];
      wantedBy = [ "multi-user.target" ];
      unitConfig.DefaultDependencies = false;
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.getExe apply;
        ExecReload = lib.getExe apply;
        CapabilityBoundingSet = [ "CAP_NET_ADMIN" ];
        RestrictAddressFamilies = [
          "AF_NETLINK"
          "AF_UNIX"
        ];
        NoNewPrivileges = true;
        PrivateDevices = true;
        PrivateTmp = true;
        ProtectHome = true;
        ProtectSystem = "strict";
        ProtectKernelLogs = true;
        ProtectControlGroups = true;
        RestrictNamespaces = true;
        RestrictRealtime = true;
        LockPersonality = true;
        SystemCallArchitectures = "native";
      };
    };
  };
}
