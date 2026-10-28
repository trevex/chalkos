# NAT64 on router images: Jool's kernel module and tool, the output jool, which renders an
# instance of Jool per NAT64 document, and chalkos-nat64.service, which brings Jool's instances and
# the routes of their prefixes to the rendered files at boot and on each delivery that changes
# them.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  jool = pkgs.jool-cli;
  # Brings Jool's instances and their routes to the files of the output jool. It checks every file
  # before it changes anything, so a file jool refuses changes no instance. A file's instance is
  # created, or replaced in place, which keeps its sessions; Jool keeps an instance's pool6, so an
  # instance of another prefix is removed first and loses its sessions. Each prefix gets an
  # unreachable route of the protocol 64, which BGP announces when a network names it, and which
  # answers its traffic while no instance takes it. Instances and routes that no file names are
  # removed; with the argument stop, every one is. Without the output's directory, as in
  # maintenance mode, no file names one. A step that fails leaves the others to run, and the
  # script then fails, naming each failed step.
  apply = pkgs.writeShellApplication {
    name = "chalkos-nat64";
    runtimeInputs = [
      jool
      pkgs.iproute2
      pkgs.jq
      pkgs.gnused
      pkgs.coreutils
    ];
    text = ''
      dir=${config.chalkos.outputs.jool.dir}
      proto=64
      declare -A want=()
      failed=()
      files=()
      if [[ ''${1:-} != stop && -d $dir ]]; then
        shopt -s nullglob
        files=("$dir"/*.json)
        shopt -u nullglob
      fi
      for file in "''${files[@]}"; do
        if ! jool file check "$file"; then
          echo "chalkos-nat64: jool refuses $file; no instance changed" >&2
          exit 1
        fi
        want[$(basename "$file" .json)]=$(jq -er .global.pool6 "$file")
      done
      for file in "''${files[@]}"; do
        name=$(basename "$file" .json)
        prefix=''${want[$name]}
        current=$( (jool -i "$name" global display --csv 2>/dev/null || true) | sed -n 's/^pool6,//p')
        if [[ -n $current && $current != "$prefix" ]]; then
          jool -i "$name" instance remove || failed+=("remove instance $name of $current")
        fi
        jool file handle "$file" || failed+=("apply instance $name")
        ip -6 route replace unreachable "$prefix" proto "$proto" || failed+=("route $prefix")
      done
      instances=$(jool instance display --csv --no-headers)
      while IFS=, read -r _ name _; do
        if [[ -n $name && ! -v want[$name] ]]; then
          jool -i "$name" instance remove || failed+=("remove instance $name")
        fi
      done <<<"$instances"
      routes=$(ip -6 -j route show proto "$proto" | jq -r '.[].dst')
      while read -r prefix; do
        keep=false
        for p in "''${want[@]}"; do
          [[ $p == "$prefix" ]] && keep=true
        done
        if [[ -n $prefix ]] && ! $keep; then
          ip -6 route del unreachable "$prefix" proto "$proto" || failed+=("remove route $prefix")
        fi
      done <<<"$routes"
      if ((''${#failed[@]} > 0)); then
        printf 'chalkos-nat64: failed to %s\n' "''${failed[@]}" >&2
        exit 1
      fi
    '';
  };
in
{
  config = lib.mkIf config.chalkos.router.enable {
    boot.extraModulePackages = [ config.boot.kernelPackages.jool ];
    # Loaded at boot, so the image's module check proves the tree holds it and what it depends on.
    boot.kernelModules = [ "jool" ];

    chalkos.outputs.jool = {
      inputs = [ "NAT64" ];
      render = "${lib.getExe chalkosPackages.chalkos-render} jool";
      # jool file check reads a file as file handle does, without the kernel.
      check = ''for f in "$OUT"/*.json; do [ ! -e "$f" ] || ${jool}/bin/jool file check "$f" || exit; done'';
      examples = ../examples/router;
    };
    chalkos.effects.jool = {
      outputs = [ "jool" ];
      reload = [ "chalkos-nat64.service" ];
    };

    systemd.services.chalkos-nat64 = {
      description = "Run the router's NAT64 instances";
      # Within one delivery the firewall reloads first, then the instances and their routes, then
      # FRR, which announces a prefix once its route exists.
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
      ];
      before = [ "frr.service" ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.getExe apply;
        ExecReload = lib.getExe apply;
        ExecStop = "${lib.getExe apply} stop";
        # jool and ip change the kernel's NAT64 instances and routes over netlink, which takes
        # CAP_NET_ADMIN and nothing more.
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
    # chalkd reads Jool's sessions, bindings and pools for the node's status.
    systemd.services.chalkd.path = [ jool ];
  };
}
