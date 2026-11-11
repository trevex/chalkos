# VRFs on router images: the output networkd renders the VRF documents beside networkd's own, with
# chalkos-render router-networkd, as a VRF device's units and VRF= on its members' Networks.
# networkd creates a VRF on reload but neither changes an existing VRF's table nor removes a VRF
# whose unit went, so chalkos-vrf removes those and reloads networkd, which then creates them anew
# with their members. networkd keeps a WireGuard device whose unit went too, with its keys and
# peers, so chalkos-vrf removes those as well.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  kinds = import ./kinds.nix { inherit lib; };
  networkd = config.chalkos.outputs.networkd.dir;
  # Removes each kernel VRF that no .netdev of Kind=vrf declares with its table, after flushing the
  # unreachable defaults that networkd put into its table, and each WireGuard device that no .netdev
  # of Kind=wireguard declares, with its keys and peers. It reloads a running networkd when it
  # removed a VRF, which creates the VRF anew with its members, and a running chalkos-resolvers,
  # which restarts the VRF's resolver on the new device. Reloading networkd itself leaves no VRF removed
  # whatever order a delivery's reloads take; a networkd that does not run, as at boot, reads every
  # unit when it starts. systemctl and networkctl come from the unit's PATH.
  apply = pkgs.writeShellApplication {
    name = "chalkos-vrf";
    runtimeInputs = [
      pkgs.iproute2
      pkgs.jq
      pkgs.gawk
      pkgs.coreutils
    ];
    text = ''
      declare -A want=() wireguards=() seen=()
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
        while IFS=$'\t' read -r kind name table; do
          if [[ $kind == vrf ]]; then
            want[$name]=$table
          else
            wireguards[$name]=1
          fi
        done < <(awk -F= '
          function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
          /^[ \t]*[#;]/ { next }
          /^[ \t]*\[/ { section = trim($0); next }
          { key = trim($1); value = trim(substr($0, index($0, "=") + 1)) }
          section == "[NetDev]" && key == "Kind" { kind = value }
          section == "[NetDev]" && key == "Name" { name = value }
          section == "[VRF]" && key == "Table" { table = value }
          END { if ((kind == "vrf" || kind == "wireguard") && name != "") printf "%s\t%s\t%s\n", kind, name, table }' "$file")
      done
      # ip link show type vrf lists every link without a kind, as empty objects, while the
      # kernel's vrf module is not loaded, so jq picks the VRFs from all links.
      vrfs=$(ip -d -j link show | jq -r '.[] | select(.linkinfo.info_kind == "vrf") | "\(.ifname) \(.linkinfo.info_data.table)"')
      failed=()
      removed=false
      while read -r name table; do
        if [[ -n $name && ''${want[$name]-} != "$table" ]]; then
          # The unreachable defaults that networkd installed, of protocol static, would stay in the
          # old table, which a VRF may take later. They go first, and the VRF only once they went,
          # so that a retry finds the VRF and flushes them again.
          if ip -4 route flush table "$table" exact 0.0.0.0/0 type unreachable proto static &&
            ip -6 route flush table "$table" exact ::/0 type unreachable proto static &&
            ip link delete "$name"; then
            removed=true
          else
            failed+=("$name")
          fi
        fi
      done <<<"$vrfs"
      # A WireGuard device of no unit holds its keys and peers, and would keep the tunnel up.
      stale=$(ip -d -j link show | jq -r '.[] | select(.linkinfo.info_kind == "wireguard") | .ifname')
      while read -r name; do
        if [[ -n $name && -z ''${wireguards[$name]-} ]] && ! ip link delete "$name"; then
          failed+=("$name")
        fi
      done <<<"$stale"
      if $removed && systemctl is-active --quiet systemd-networkd.service && ! networkctl reload; then
        echo 'chalkos-vrf: failed to reload networkd, which creates the removed VRFs anew' >&2
        exit 1
      fi
      # A reload of chalkos-resolvers that ran before this unit, as when chalkd restarts a failed
      # chalkos-vrf after its reloads, saw the old VRF. --no-block, as chalkos-resolvers orders
      # after this unit and its reload waits for this run to end.
      if $removed && systemctl is-active --quiet chalkos-resolvers.service &&
        ! systemctl reload --no-block chalkos-resolvers.service; then
        echo 'chalkos-vrf: failed to reload chalkos-resolvers, which restarts the resolvers of the removed VRFs' >&2
        exit 1
      fi
      if ((''${#failed[@]} > 0)); then
        printf 'chalkos-vrf: failed to remove the device %s\n' "''${failed[@]}" >&2
        exit 1
      fi
    '';
  };
in
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.kinds = kinds;

    # The output is networkd's, whose directory networkd reads; the router layer renders it with
    # the VRF documents.
    chalkos.outputs.networkd = {
      inputs = lib.attrNames kinds;
      render = "${lib.getExe chalkosPackages.chalkos-render} router-networkd ${config.system.build.chalkosNetworkdSchemas}";
    };
    chalkos.effects.vrf = {
      outputs = [ "networkd" ];
      reload = [ "chalkos-vrf.service" ];
    };

    # A delivery reloads chalkos-vrf and networkd with one systemctl call, whose job of each unit
    # takes this order.
    systemd.services.chalkos-vrf = {
      description = "Remove the VRFs and WireGuard devices the config no longer declares";
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
