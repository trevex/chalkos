# The router's firewall: the nftables table inet chalkos, which the output router-nftables renders
# from the node's router documents, in place of NixOS's firewall. chalkos-firewall loads it before
# the network comes up, and a fallback ruleset when the node has none rendered; the output's
# effect reloads it, in one transaction, when it changes.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  render = lib.getExe chalkosPackages.chalkos-render;
  nft = "${pkgs.nftables}/bin/nft";
  unshare = "${pkgs.util-linux}/bin/unshare";
  # chalkd's port, which modules/node/chalkd.nix opens in NixOS's firewall on other images.
  chalkdPort = "50000";
  ruleset = "${config.chalkos.outputs.router-nftables.dir}/ruleset.nft";
  # The ruleset of a node without a rendered one, as in maintenance mode or when its config did
  # not render: the protected rules and chalkd's port on every interface, and no forwarding.
  fallback = pkgs.runCommand "chalkos-firewall-fallback.nft" { } ''
    mkdir out
    echo '{"node": {"name": ""}, "documents": {}}' | OUT=$PWD/out ${render} router-nftables-fallback ${chalkdPort}
    ${unshare} --map-root-user --net ${nft} --check --file out/ruleset.nft
    cp out/ruleset.nft $out
  '';
  # A rendered ruleset that nft refuses leaves the fallback in place and fails the unit.
  load = pkgs.writeShellScript "chalkos-firewall-load" ''
    if [ ! -e ${ruleset} ]; then
      echo "the node has no rendered ruleset; loading the fallback ruleset, which forwards nothing" >&2
      exec ${nft} -f ${fallback}
    fi
    ${nft} -f ${ruleset} && exit 0
    echo "nft refused ${ruleset}; loading the fallback ruleset, which forwards nothing" >&2
    ${nft} -f ${fallback}
    exit 1
  '';
in
{
  config = lib.mkIf config.chalkos.router.enable {
    # chalkos-firewall loads the one table the router filters with; NixOS's nftables service
    # would load nothing.
    networking.firewall.enable = false;
    networking.nftables.enable = lib.mkForce false;

    chalkos.outputs.router-nftables = {
      inputs = [
        "Router"
        "FirewallZone"
        "FirewallPolicy"
        "AddressSet"
        "NATRule"
      ];
      render = "${render} router-nftables ${chalkdPort}";
      # nft checks a ruleset with the kernel, which takes CAP_NET_ADMIN; the check has it in a
      # user and network namespace of its own.
      check = "${unshare} --map-root-user --net ${nft} --check --file \"$OUT/ruleset.nft\"";
      examples = ../examples/router;
    };
    chalkos.effects.router-nftables = {
      outputs = [ "router-nftables" ];
      reload = [ "chalkos-firewall.service" ];
    };

    # Ordered as NixOS's nftables service, and after chalkos-config, which renders the ruleset.
    systemd.services.chalkos-firewall = {
      description = "Load the router's firewall";
      after = [
        "sysinit.target"
        "chalkos-config.service"
      ];
      before = [
        "network-pre.target"
        "shutdown.target"
      ];
      conflicts = [ "shutdown.target" ];
      wants = [
        "network-pre.target"
        "sysinit.target"
      ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = load;
        # A ruleset nft refuses leaves the loaded one in place.
        ExecReload = "${nft} -f ${ruleset}";
      };
    };
  };
}
