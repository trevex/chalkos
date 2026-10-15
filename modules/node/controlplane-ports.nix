# Opens the control plane's ports on a control-plane node and makes etcd's data directory, by the
# node's type from typeCommand. The NixOS firewall drops what its own input chain does not
# accept, whatever other tables do, so the table chalkos-controlplane only marks TCP to 6443,
# 2379 and 2380, and the firewall accepts what carries the mark (see kubernetes.nix). The table
# is not the firewall's: reloading the firewall leaves it alone. On a worker the table goes.
{
  lib,
  writeShellApplication,
  coreutils,
  nftables,
  typeCommand,
  etcdDir ? "/var/lib/etcd",
  # The packet mark bit the firewall accepts the ports with.
  mark,
}:
let
  clear = "meta mark & ${mark} == ${mark} meta mark set meta mark ^ ${mark}";
in
writeShellApplication {
  name = "chalkos-controlplane";
  runtimeInputs = [
    coreutils
    nftables
  ];
  text = ''
    type=$(${typeCommand})
    if [ "$type" = controlplane ]; then
      install -d -m 0700 ${lib.escapeShellArg etcdDir}
      # Its input chain runs before the firewall's and first clears the mark, so only this rule
      # sets it; its clear chain runs after the firewall's, so the mark never outlives it.
      nft -f - <<'NFT'
    table inet chalkos-controlplane
    delete table inet chalkos-controlplane
    table inet chalkos-controlplane {
      chain input {
        type filter hook input priority filter - 1; policy accept;
        ${clear}
        tcp dport { 6443, 2379, 2380 } meta mark set meta mark | ${mark}
      }
      chain clear {
        type filter hook input priority filter + 1; policy accept;
        ${clear}
      }
    }
    NFT
    else
      if nft list table inet chalkos-controlplane >/dev/null 2>&1; then
        nft delete table inet chalkos-controlplane
      fi
    fi
  '';
}
