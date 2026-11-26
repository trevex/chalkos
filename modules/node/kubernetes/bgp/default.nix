# BGP on Kubernetes images whose chalkos.kubernetes.bgp.enable is true: FRR (modules/node/frr)
# runs the sessions of the node's BGPSpeaker with its BGPPeers, whose config the output bgp
# renders. FRR announces the addresses of the speaker's export interfaces, the node's own and the
# VIP while the node holds it on one of them, and installs the default routes its peers send. A
# node whose config holds no BGPSpeaker runs no FRR.
{
  chalkosLib,
  config,
  kubernetesLib,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosLib.internal) cel;
  inherit (chalkosLib.internal.api) apiVersions;
  kinds = import ./kinds.nix { inherit chalkosLib lib; };
  render = lib.getExe pkgs.chalkos.chalkos-render;
  nft = lib.getExe pkgs.nftables;
  firewall = config.networking.firewall.enable;
  # The packet mark bit of BFD's port, which the table of the output bfd-firewall sets; see
  # firewall.nix for the other marks.
  mark = "0x08000000";
  bfdTable = "${config.chalkos.outputs.bfd-firewall.dir}/bfd.nft";
  # Whether the config c holds a BGPSpeaker.
  speaker = c: "size(${cel.documents c apiVersions.kubernetes "BGPSpeaker"}) > 0";
in
{
  options.chalkos.kubernetes = kubernetesLib.feature "bgp" {
    carries = "FRR, which runs BGP and BFD,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.kubernetes.enable && config.chalkos.kubernetes.bgp.enable) (
    lib.mkMerge [
      {
        chalkos.kinds = kinds;
        chalkos.tests = import ./tests.nix { inherit firewall; };

        chalkos.frr = {
          enable = true;
          rendered = "${config.chalkos.outputs.bgp.dir}/frr.conf";
          # The config of a node without a rendered one, as in maintenance mode: FRR runs no BGP.
          fallback = pkgs.runCommand "frr-fallback.conf" { } ''
            mkdir out
            echo '{"node": {"name": ""}, "documents": {}}' | OUT=$PWD/out ${render} kubernetes-frr
            cp out/frr.conf $out
          '';
          # The output bgp holds it with a BGPSpeaker alone.
          marker = "${config.chalkos.outputs.bgp.dir}/speaker";
        };

        # The speaker and its peers, and the Node, whose IPv4 address is the router ID of a speaker
        # that gives none.
        chalkos.outputs.bgp = {
          inputs = lib.attrNames kinds ++ [ "Node" ];
          render = "${render} kubernetes-frr";
          inherit (config.chalkos.frr) check;
        };
        # A reload of a failed frr.service restarts it, which fills the config in as a reload does.
        chalkos.effects.bgp = {
          outputs = [ "bgp" ] ++ lib.optional firewall "bfd-firewall";
          reload = [ "frr.service" ];
          after = [
            "networkd"
            "links"
          ];
        };
        # A delivery that adds the first BGPSpeaker starts FRR, and one that removes the last stops
        # it: the restart finds the marker, or not.
        chalkos.effects.bgp-speaker = {
          when = "(${speaker "config"}) != (${speaker "oldConfig"})";
          restart = [ "frr.service" ];
          start = true;
          after = [
            "networkd"
            "links"
          ];
        };
        # chalkd loads the values the peers' password fields name as frr.service's credentials.
        systemd.services.frr.chalkos.kinds = [ "BGPPeer" ];
      }
      (lib.mkIf firewall {
        # BFD's control packets arrive on UDP port 3784, from a source port of the neighbour's
        # choosing, so no connection the firewall tracks covers them. The NixOS firewall drops
        # what its own input chain does not accept, so the table of the output bfd-firewall marks
        # them from the BGPPeers with bfd alone, and the firewall accepts what carries the mark.
        chalkos.outputs.bfd-firewall = {
          inputs = [
            "BGPPeer"
            "BGPSpeaker"
          ];
          render = "${render} kubernetes-bfd ${mark}";
          # nft checks a table with the kernel, which takes CAP_NET_ADMIN; the check has it in a
          # user and network namespace of its own. A node without a BGPSpeaker gets no table.
          check = "${render} check each bfd.nft ${pkgs.util-linux}/bin/unshare --map-root-user --net ${nft} --check --file $FILE";
        };
        # frr.service loads the table before FRR starts and on each reload, before FRR takes new
        # peers, and deletes it when it stops; + runs the commands as root with every capability.
        systemd.services.frr = {
          after = [ "nftables.service" ];
          serviceConfig = {
            ExecStartPre = "+${nft} -f ${bfdTable}";
            ExecReload = lib.mkBefore [ "+${nft} -f ${bfdTable}" ];
            ExecStopPost = "-+${nft} delete table inet chalkos-bfd";
          };
        };
        networking.firewall.extraInputRules = "udp dport 3784 meta mark & ${mark} == ${mark} accept";
      })
    ]
  );
}
