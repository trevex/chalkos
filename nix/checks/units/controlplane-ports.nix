# The worker's control-plane ports with its firewall, in a network namespace: a peer connects to
# 6443, 2379 and 2380 over IPv4 and IPv6, which the firewall accepts once the node's Kubernetes
# Node document makes it a control plane, and refuses again once it makes it a worker.
{
  checkLib,
  worker,
  firewall,
  unitCheck,
  ...
}:
let
  script = worker.config.systemd.services.chalkos-controlplane.serviceConfig.ExecStart;
in
unitCheck "controlplane-ports" checkLib.networkTools ''
  debug() {
    nft list ruleset >&2
  }
  ${checkLib.peerNamespace}

  # The firewall as NixOS loads it, with the state file it keeps.
  mkdir -p /run/nftables
  touch /run/nftables/deletions.nft
  nft -f ${firewall}
  for port in 6443 2379 2380; do
    socat TCP4-LISTEN:$port,fork,reuseaddr /dev/null &
    socat TCP6-LISTEN:$port,fork,reuseaddr,ipv6only=1 /dev/null &
  done
  listening() { [ "$(ss -Hltn | wc -l)" = 6 ]; }
  retry 100 "" listening || {
    ss -Hltn >&2
    fail "the listeners did not start"
  }
  reaches() { on_peer socat -u /dev/null "TCP:$1:$2,connect-timeout=1" 2>/dev/null; }
  open() {
    for port in 6443 2379 2380; do
      reaches 10.0.0.11 $port || fail "$port refused $1"
      reaches '[fd00::11]' $port || fail "$port refused over IPv6 $1"
    done
  }
  closed() {
    for port in 6443 2379 2380; do
      if reaches 10.0.0.11 $port; then fail "$port open $1"; fi
      if reaches '[fd00::11]' $port; then fail "$port open over IPv6 $1"; fi
    done
  }
  node=/run/chalkos/config/kubernetes.chalkos.dev/Node/default.json
  mkdir -p "$(dirname "$node")"
  node_type() {
    printf '{"apiVersion":"kubernetes.chalkos.dev/v1alpha1","kind":"Node","metadata":{"name":"default"},"spec":{"type":"%s"}}\n' "$1" >"$node"
  }

  closed "without the table"
  node_type controlplane
  ${script}
  [ "$(stat -c %a /var/lib/etcd)" = 700 ] || fail "no etcd directory"
  open "on a control plane"
  # The firewall's reload leaves the table alone.
  nft -f ${firewall}
  open "after the firewall's reload"
  node_type worker
  ${script}
  closed "on a worker"
  # A worker without the table stays one.
  ${script}
  closed "on a worker again"
''
