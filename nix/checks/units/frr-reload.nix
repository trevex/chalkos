# FRR's units on a VM without /bin/sh, which FRR's own scripts and watchfrr would run. For pairs
# of the renderers' goldens, frr.service starts FRR on one and its reload, chalkos-frr-reload,
# applies the other in one pass that vtysh takes whole, after which FRR's running config is the
# one FRR shows when it starts on the second, and for which the reload plans nothing. Then FRR
# runs a BGP session with a password to a peer in a network namespace: after bgpd is killed, and
# after zebra is killed, which stops its clients, systemd starts the daemons again, they load
# their config again and the session, its route and a static route come back.
{ pkgs, lib, ... }:
let
  inherit (pkgs.chalkos) frr;
  reload = "${lib.getExe pkgs.chalkos.chalkos-frr-reload} -vtysh vtysh";
  # The daemons, each in a unit of its own that loads its config before it is active.
  daemons = [
    "mgmtd"
    "zebra"
    "staticd"
    "bfdd"
    "bgpd"
  ];
  # The rendered config, which frr.service fills in.
  rendered = "/run/frr-test/frr.conf";
  # The crash test's config: a peer group's password, which the fill fills in, a session with the
  # peer and a static route.
  crash = pkgs.writeText "crash.conf" ''
    frr defaults traditional
    log syslog informational
    ip route 203.0.113.0/24 blackhole
    router bgp 65001
     bgp router-id 192.0.2.1
     no bgp ebgp-requires-policy
     neighbor peer-fabric peer-group
     neighbor peer-fabric remote-as 65002
     neighbor peer-fabric password @secret_bgp.fabric@
     neighbor peer-fabric timers connect 1
     neighbor 192.0.2.2 peer-group peer-fabric
    exit
  '';
  # The peer, a zebra and bgpd of its own in a network namespace, announces a prefix.
  peer = pkgs.writeText "peer.conf" ''
    frr defaults traditional
    hostname peer
    router bgp 65002
     bgp router-id 192.0.2.2
     no bgp ebgp-requires-policy
     no bgp network import-check
     neighbor 192.0.2.1 remote-as 65001
     neighbor 192.0.2.1 password example-password
     neighbor 192.0.2.1 timers connect 1
     address-family ipv4 unicast
      network 198.51.100.0/24
     exit-address-family
    exit
  '';
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
pkgs.testers.runNixOSTest {
  name = "chalkos-frr-reload";
  nodes.machine =
    { lib, ... }:
    {
      imports = [ ../../../modules/node/frr ];
      # The runtime's option, which the FRR module's diagnostics set.
      options.chalkos.diagnostics = lib.mkOption {
        type = lib.types.attrsOf lib.types.anything;
        default = { };
      };
      config = {
        # The module argument images get, whose sandbox the units take.
        _module.args.chalkosLib = import ../../../lib/chalkos.nix { inherit lib; };
        chalkos.frr = {
          enable = true;
          inherit rendered;
          fallback = goldens.router-none;
        };
        environment.systemPackages = [ pkgs.jq ];
        # The test starts FRR on each config, with the credentials chalkd's grant would load.
        systemd.services = {
          frr = {
            wantedBy = lib.mkForce [ ];
            startLimitIntervalSec = 0;
            serviceConfig.LoadCredential = [ "secret:/run/chalkos/credentials/frr" ];
          };
        }
        // lib.genAttrs (map (d: "frr-${d}") daemons) (_: {
          startLimitIntervalSec = 0;
        });
      };
    };
  testScript = ''
    import difflib

    goldens = {${lib.concatStrings (lib.mapAttrsToList (n: f: ''"${n}": "${f}", '') goldens)}}

    def plan():
        return machine.succeed("${reload} -n /run/frr/frr.conf")

    # The daemons take their config once they run, after frr.service has started, and vtysh shows
    # the config of the daemons that run alone, so FRR runs a config once each daemon's unit is
    # active and the reload plans nothing for it, which also shows that a config FRR runs holds no
    # change.
    def start(name):
        machine.succeed(f"install -m 644 {goldens[name]} ${rendered} && systemctl restart frr")
        for unit in [${lib.concatMapStrings (d: ''"frr-${d}.service", '') daemons}]:
            machine.wait_for_unit(unit)
        machine.wait_until_succeeds("p=$(${reload} -n /run/frr/frr.conf) && test -z \"$p\"", timeout=60)

    def pid(daemon):
        return machine.succeed(f"systemctl show -P MainPID frr-{daemon}.service").strip()

    machine.wait_for_unit("multi-user.target")
    with subtest("the VM has no /bin/sh"):
        machine.succeed("rm -f /bin/sh && test ! -e /bin/sh")
        machine.succeed("mkdir -p /run/frr-test /run/chalkos/credentials/frr")
        machine.succeed("printf example-password >/run/chalkos/credentials/frr/bgp.fabric")
        machine.succeed("printf example-password >/run/chalkos/credentials/frr/bgp.password")

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
            cursor = machine.succeed("journalctl -n 1 -o export | sed -n 's/^__CURSOR=//p'").strip()
            machine.succeed(f"install -m 644 {goldens[b]} ${rendered} && systemctl reload frr")
            journal = f"journalctl -u frr.service -o cat --after-cursor='{cursor}'"
            machine.wait_until_succeeds(f"{journal} | grep -q 'chalkos-frr-reload: FRR runs frr.conf'", timeout=30)
            out = machine.succeed(journal)
            print(out)
            if "vtysh refused commands" in out or "pass 2" in out:
                raise Exception(f"{a} reloaded to {b} in more than one clean pass")
            got = machine.succeed("${running}")
            if got != fresh[b]:
                diff = "".join(difflib.unified_diff(fresh[b].splitlines(True), got.splitlines(True), "fresh", "reloaded"))
                raise Exception(f"{a} reloaded to {b} differs from {b} loaded fresh:\n{diff}")
            if p := plan():
                raise Exception(f"after the reload to {b}, the reload plans:\n{p}")

    def session():
        try:
            machine.wait_until_succeeds(
                "vtysh -c 'show bgp neighbors 192.0.2.2 json' | jq -e '.\"192.0.2.2\".bgpState == \"Established\"'",
                timeout=60,
            )
        except Exception:
            print(machine.execute("vtysh -c 'show running-config'; vtysh -c 'show bgp neighbors'; ip netns exec peer vtysh --vty_socket /run/peer -c 'show bgp neighbors'; journalctl -u peer-bgpd.service")[1])
            raise
        machine.wait_until_succeeds("ip route show 198.51.100.0/24 proto bgp | grep -q .", timeout=30)
        machine.wait_until_succeeds("ip route show 203.0.113.0/24 | grep -q blackhole", timeout=30)

    with subtest("FRR runs a session with a password to a peer"):
        machine.succeed(
            "ip netns add peer",
            "ip link add veth0 type veth peer name veth1 netns peer",
            "ip address add 192.0.2.1/24 dev veth0 && ip link set veth0 up",
            "ip -n peer address add 192.0.2.2/24 dev veth1 && ip -n peer link set veth1 up && ip -n peer link set lo up",
            "install -d -o frr -g frr /run/peer /run/frr/peer",
            # -N peer gives the peer's daemons a mgmtd socket of their own, which nothing serves;
            # without it the peer's zebra registers with the node's mgmtd as a second zebra, and
            # the two take turns until mgmtd aborts.
            "systemd-run --unit=peer-zebra ip netns exec peer ${frr}/libexec/frr/zebra -N peer -f /dev/null"
            + " -i /run/peer/zebra.pid -z /run/peer/zserv.api --vty_socket /run/peer -A 127.0.0.1",
            "systemd-run --unit=peer-bgpd ip netns exec peer ${frr}/libexec/frr/bgpd -N peer -f ${peer}"
            + " -i /run/peer/bgpd.pid -z /run/peer/zserv.api --vty_socket /run/peer -A 127.0.0.1",
        )
        machine.succeed("install -m 644 ${crash} ${rendered} && systemctl restart frr")
        session()
        before = machine.succeed("${running}")

    with subtest("a killed bgpd starts again with its config"):
        old = pid("bgpd")
        machine.succeed(f"kill -9 {old}")
        machine.wait_until_succeeds(f"p=$(systemctl show -P MainPID frr-bgpd.service) && [ \"$p\" != 0 ] && [ \"$p\" != {old} ]", timeout=30)
        machine.wait_for_unit("frr-bgpd.service")
        session()
        if (got := machine.succeed("${running}")) != before:
            raise Exception(f"after bgpd's restart, FRR runs:\n{got}")

    with subtest("a killed zebra restarts its clients, which start again with their config"):
        old = {d: pid(d) for d in ["zebra", "bgpd", "staticd", "bfdd"]}
        mgmtd = pid("mgmtd")
        machine.succeed(f"kill -9 {old['zebra']}")
        for d, p in old.items():
            machine.wait_until_succeeds(f"n=$(systemctl show -P MainPID frr-{d}.service) && [ \"$n\" != 0 ] && [ \"$n\" != {p} ]", timeout=30)
            machine.wait_for_unit(f"frr-{d}.service")
        if pid("mgmtd") != mgmtd:
            raise Exception("zebra's failure restarted mgmtd")
        session()
        if (got := machine.succeed("${running}")) != before:
            raise Exception(f"after zebra's restart, FRR runs:\n{got}")
  '';
}
