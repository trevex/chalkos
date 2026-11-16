# chalkos-frr-reload on a VM that runs FRR, without links or peers: for pairs of the renderers'
# goldens it starts FRR on one and reloads it to the other in one pass that vtysh takes whole,
# after which FRR's running config is the one FRR shows when it starts on the second, and for which
# the reload plans nothing.
{ pkgs, lib, ... }:
let
  reload = "${lib.getExe pkgs.chalkos.chalkos-frr-reload} -vtysh vtysh";
  edge = ../../../modules/node/router/tests/edge/frr/frr.conf;
  goldens = {
    router-none = ../../../modules/node/router/tests/none/frr/frr.conf;
    router-leaf = ../../../modules/node/router/tests/leaf/frr/frr.conf;
    router-edge = edge;
    router-vrf = ../../../modules/node/router/tests/vrf/frr/frr.conf;
    router-tracked = ../../../cmd/chalkos-render/testdata/router/tracked.frr;
    kubernetes-none = ../../../modules/node/kubernetes/bgp/tests/none/bgp/frr.conf;
    kubernetes-mixed = ../../../modules/node/kubernetes/bgp/tests/mixed/bgp/frr.conf;
    kubernetes-address = ../../../modules/node/kubernetes/bgp/tests/address/bgp/frr.conf;
    kubernetes-address-interface = ../../../modules/node/kubernetes/bgp/tests/address-interface/bgp/frr.conf;
    kubernetes-unnumbered = ../../../modules/node/kubernetes/bgp/tests/unnumbered/bgp/frr.conf;
    # A peer's new password and a route-map entry less.
    router-edge-changed = pkgs.runCommand "edge-changed.conf" { } ''
      sed -e 's/^\( neighbor peer-fabric password\) .*/\1 rotated/' \
        -e '/^route-map rp-nat64 deny 65535$/,/^exit$/d' ${edge} >$out
    '';
    # The network's route-map renamed, which the reload changes in place.
    router-edge-renamed = pkgs.runCommand "edge-renamed.conf" { } ''
      sed 's/rp-nat64\b/rp-nat64-v2/' ${edge} >$out
    '';
    # A community-list of three entries, which FRR keys by value; the same list without its
    # first, whose entries shift down one sequence number; and the list reordered.
    router-edge-list = communities [
      "65001:666"
      "65001:667"
      "65001:668"
    ];
    router-edge-list-shifted = communities [
      "65001:667"
      "65001:668"
    ];
    router-edge-list-reordered = communities [
      "65001:668"
      "65001:666"
      "65001:667"
    ];
  };
  # The edge golden with the community-list cl-blackhole of these communities.
  communities =
    values:
    pkgs.runCommand "edge-communities.conf" { } ''
      awk '/^bgp community-list standard cl-blackhole / {
        ${lib.concatImapStrings (
          i: v: ''print "bgp community-list standard cl-blackhole seq ${toString (5 * i)} permit ${v}"; ''
        ) values}
        next
      } { print }' ${edge} >$out
    '';
  # Pairs of goldens, the first FRR starts on and the second it reloads to: peers, peer groups,
  # route-map entries, lists, address families, interfaces, BFD profiles and VRF instances that
  # come and go, a renamed route-map, a community-list's entries that shift or reorder, and a new
  # AS number.
  transitions = [
    [
      "router-none"
      "router-edge"
    ]
    [
      "router-edge"
      "router-leaf"
    ]
    [
      "router-leaf"
      "router-edge"
    ]
    [
      "router-edge"
      "router-edge-changed"
    ]
    [
      "router-edge"
      "router-edge-renamed"
    ]
    [
      "router-edge"
      "router-edge-list"
    ]
    [
      "router-edge-list"
      "router-edge-list-shifted"
    ]
    [
      "router-edge-list"
      "router-edge-list-reordered"
    ]
    [
      "router-edge-list-shifted"
      "router-edge-list"
    ]
    [
      "router-edge"
      "router-vrf"
    ]
    [
      "router-vrf"
      "router-tracked"
    ]
    [
      "router-tracked"
      "router-vrf"
    ]
    [
      "router-vrf"
      "router-none"
    ]
    [
      "router-edge"
      "kubernetes-mixed"
    ]
    [
      "kubernetes-none"
      "kubernetes-mixed"
    ]
    [
      "kubernetes-mixed"
      "kubernetes-unnumbered"
    ]
    [
      "kubernetes-unnumbered"
      "kubernetes-mixed"
    ]
    [
      "kubernetes-mixed"
      "kubernetes-address-interface"
    ]
    [
      "kubernetes-address-interface"
      "kubernetes-address"
    ]
    [
      "kubernetes-address"
      "kubernetes-mixed"
    ]
    [
      "kubernetes-mixed"
      "kubernetes-none"
    ]
  ];
  # FRR's running config as lines that each name their contexts, sorted: FRR orders some
  # commands by when they were given. The hostname, which a plan sets and a start leaves as the
  # VM's, is left out.
  running = pkgs.writeShellScript "frr-running" ''
    vtysh -c 'show running-config' | ${lib.getExe pkgs.gawk} '
      /^(Building configuration|Current configuration:|frr version |hostname )/ || /^ *(!|exit|exit-address-family|exit-vrf|end)$/ || NF == 0 { next }
      {
        match($0, /^ */)
        depth = RLENGTH
        path[depth] = $0
        prefix = ""
        for (i = 0; i < depth; i++) if (i in path) prefix = prefix path[i] " >"
        # FRR shows the bfd node once it had a profile; its profiles name it.
        if ($0 != "bfd") print prefix $0
      }' | sort
  '';
in
(pkgs.extend (final: _: { inherit (final.chalkos) frr; })).testers.runNixOSTest {
  name = "chalkos-frr-reload";
  nodes.machine = {
    services.frr = {
      bgpd.enable = true;
      bfdd.enable = true;
      configFile = "/run/frr/frr.conf";
    };
    # The test starts FRR on each config.
    systemd.services.frr = {
      wantedBy = lib.mkForce [ ];
      startLimitIntervalSec = lib.mkForce 0;
    };
  };
  testScript = ''
    import difflib

    goldens = {${lib.concatStrings (lib.mapAttrsToList (n: f: ''"${n}": "${f}", '') goldens)}}

    def plan():
        return machine.succeed("${reload} -n /run/frr/frr.conf")

    # The daemons take their config after frr.service has started, so FRR runs a config once the
    # reload plans nothing for it, which also shows that a config FRR runs holds no change.
    def start(name):
        machine.succeed(f"install -m 644 {goldens[name]} /run/frr/frr.conf && systemctl restart frr")
        machine.wait_until_succeeds("p=$(${reload} -n /run/frr/frr.conf) && test -z \"$p\"", timeout=60)

    machine.wait_for_unit("multi-user.target")
    machine.succeed("mkdir -p /run/frr")
    fresh = {}
    for name in goldens:
        with subtest(f"FRR runs {name}"):
            start(name)
            fresh[name] = machine.succeed("${running}")

    for a, b in [${
      lib.concatMapStrings (t: ''("${lib.elemAt t 0}", "${lib.elemAt t 1}"), '') transitions
    }]:
        with subtest(f"{a} reloads to {b}"):
            start(a)
            machine.succeed(f"install -m 644 {goldens[b]} /run/frr/frr.conf")
            out = machine.succeed("${reload} /run/frr/frr.conf 2>&1")
            print(out)
            if "vtysh refused commands" in out or "pass 2" in out:
                raise Exception(f"{a} reloaded to {b} in more than one clean pass")
            got = machine.succeed("${running}")
            if got != fresh[b]:
                diff = "".join(difflib.unified_diff(fresh[b].splitlines(True), got.splitlines(True), "fresh", "reloaded"))
                raise Exception(f"{a} reloaded to {b} differs from {b} loaded fresh:\n{diff}")
            if p := plan():
                raise Exception(f"after the reload to {b}, the reload plans:\n{p}")
  '';
}
