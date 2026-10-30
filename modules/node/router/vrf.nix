# VRFs on router images: the output networkd renders the VRF documents beside networkd's own, with
# chalkos-render router-networkd, as a VRF device's units and VRF= on its members' Networks. networkd
# creates a VRF on reload but neither changes an existing VRF's table nor removes a VRF whose unit
# went, so chalkos-vrf removes those before networkd's reload, which then creates them anew with
# their members.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  networkd = config.chalkos.outputs.networkd.dir;
  # Removes each kernel VRF that no rendered .netdev of Kind=vrf declares with its table.
  apply = pkgs.writeShellApplication {
    name = "chalkos-vrf";
    runtimeInputs = [
      pkgs.iproute2
      pkgs.jq
      pkgs.gawk
      pkgs.coreutils
    ];
    text = ''
      declare -A want=()
      shopt -s nullglob
      for file in ${networkd}/*.netdev; do
        read -r kind name table < <(awk -F= '
          /^\[/ { section = $0; next }
          section == "[NetDev]" && $1 == "Kind" { kind = $2 }
          section == "[NetDev]" && $1 == "Name" { name = $2 }
          section == "[VRF]" && $1 == "Table" { table = $2 }
          END { print kind, name, table }' "$file")
        if [[ $kind == vrf && -n $name ]]; then
          want[$name]=$table
        fi
      done
      vrfs=$(ip -d -j link show type vrf | jq -r '.[] | "\(.ifname) \(.linkinfo.info_data.table)"')
      failed=()
      while read -r name table; do
        if [[ -n $name && ''${want[$name]-} != "$table" ]]; then
          ip link delete "$name" || failed+=("$name")
        fi
      done <<<"$vrfs"
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

    # A delivery reloads chalkos-vrf and networkd in one job, which takes this order.
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
