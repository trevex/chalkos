---
title: "Announce addresses with BGP"
description: "Announce a Kubernetes node's addresses and the routed VIP with the layer's BGP speaker"
---

# Announce addresses with BGP

This page shows how to announce a Kubernetes node's addresses to the routers of its network over
BGP. Each node runs FRR, the Kubernetes layer's BGP speaker. It announces the addresses of the
interfaces its config names, such as the node's own address on a dummy interface and the API's VIP
while the node holds it, and can take the default route its peers send. Use it when the nodes sit
on a routed fabric instead of one layer-2 segment, when a node's address should stay reachable
over whichever of its ports works, or when the VIP runs in the `routed` mode.

Images with the Kubernetes layer carry the speaker by default. FRR does not run until the node's
config holds a `BGPSpeaker`, and the firewall opens BFD's port to the `BGPPeer`s with `bfd`
alone.

## Before you begin

You need:

- nodes installed with an image with the Kubernetes layer, and their files, as
  [Bootstrap a cluster](bootstrap.md) sets them up;
- an AS number for the nodes, and the AS numbers and addresses of the routers they peer with;
- routers that accept the nodes' sessions. A chalkos router does so with a `BGPPeer` of its own,
  as [Route with BGP](../router/bgp.md) describes;
- for a routed VIP, the `Cluster` of
  [Run a highly available control plane](ha-control-plane.md#announce-the-vip-over-bgp).

## Declare the node's speaker

A node holds one `BGPSpeaker`. It gives the node's AS number and router ID, and names the
interfaces whose addresses FRR announces to every peer. This node's address sits on the dummy
interface `node0`:

```yaml title="nodes/cp1.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: BGPSpeaker
metadata: {name: bgp}
spec:
  asn: 65001
  routerID: 0.0.0.11
  export: {interfaces: [node0]}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: NetDev
metadata: {name: 30-node0}
spec:
  netDev: {name: node0, kind: dummy}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 30-node0}
spec:
  match: {name: node0}
  network: {address: ["fdc8:a1b2:c3d4:ff::11/128"], keepConfiguration: "yes"}
```

FRR announces the connected routes of the interfaces in `export.interfaces`. An address of /32 or
/128 is announced as itself, and an address with a shorter prefix as its subnet. An address added
to such an interface later is announced at once, and withdrawn when it goes. A dummy interface
keeps the node's address up while any of its ports works. Each export interface needs a `Network`
document whose `match.name` names it; a glob such as `node*` does not count.

The router ID is an IPv4 address that is unique within the node's AS. A node whose `Node` document
lists an IPv4 address in `spec.nodeIP.addresses` can leave `routerID` out, and FRR takes the first
such address. An IPv6-only node gives one.

## Add the peers

A `BGPPeer` is a neighbour of the node. A peer takes one of three forms:

| Fields | Sessions |
| --- | --- |
| `interfaces` alone | An unnumbered session on each interface, to the neighbour's IPv6 link-local address. |
| `address` and one interface | One session to the address, bound to the interface. |
| `address` alone | One session to the address, over the route the kernel picks. |

Each interface a peer names needs a `Network` document whose `match.name` names it, as an export
interface does.

Unnumbered sessions need no addresses on the links. This peer runs a session on each of the node's
two ports:

```yaml
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: BGPPeer
metadata: {name: fabric}
spec:
  asn: 65000
  interfaces: [enp1s0, enp2s0]
```

FRR keys a session by the neighbour's address, so a peer with an address runs one session. Two
ports to one router take two peers, each with an address of the router and one of the ports. The
homelab peers this way, and its file also holds the ports' `Network` documents:

```yaml title="bgp/fabric.yaml"
--8<-- "examples/homelab/bgp/fabric.yaml"
```

A peer with an address alone suits a router on a subnet of the node. This peer is the router `r1`
at `fd00:300::1`:

```yaml title="nodes/k1.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: BGPPeer
metadata: {name: r1}
spec:
  asn: 64512
  address: "fd00:300::1"
  password: {secretRef: {name: bgp, key: fabric}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: bgp}
spec:
  data: {fabric: lab-s3cret}
```

`asn` is the neighbour's AS number, which the node refuses to change in place; a neighbour in
another AS is a peer of another name. `password` names a key of a `Secret` that holds the
sessions' TCP MD5 password, of 1 to 80 printable characters without white space.
[Keep the services' secrets in Secret documents](../operate/secrets.md#keep-the-services-secrets-in-secret-documents)
shows the homelab's. `bfd`, on by default, ends a session within about a second of its link
failing.
`import` sets what the node takes from the neighbour: `default`, the default, takes its default
routes alone, and `none` takes nothing.

An unnumbered session carries IPv4 and IPv6 routes, with IPv6 next hops for IPv4. So does a
session to an IPv6 address bound to an interface. A session to an IPv6 address without an
interface carries IPv6 alone, and one to an IPv4 address carries IPv4 alone. The node announces
only the routes it originates, so it never passes one peer's routes on to another.

## Take the default route from the peers

With `import: default`, FRR takes the default routes the peers send. From peers in one AS it
installs them as one route with a next hop per session; from peers in different ASes it installs
the best path alone. zebra installs a default route from BGP only while the node has no
other one. A default route from a router advertisement or a DHCP lease wins, and the BGP route
stays in FRR's table unused.

networkd can give SLAAC and DHCP addresses the `noprefixroute` flag and install their subnet
routes itself. FRR sees these as kernel routes rather than connected routes. Numbered peers use
`disable-connected-check` so FRR can resolve the neighbour through those routes. Sessions still
use a TTL of 1 and, when configured, bind to their interface; this does not enable multihop.

For the default route to come from BGP, leave the gateway out of the fabric's `Network` documents:
`ipv6AcceptRA: {useGateway: false}` for router advertisements, `dhcpv4: {useGateway: false}` for
DHCP, and no `gateway`. Then BFD takes a failed port's next hop away within about a second. A node
with no session has no default route, so give it a static route to the networks you manage it
from. The homelab above uses router advertisements for the default route and sets
`import: none` on the BGP peers. A workstation can reach a node at its fabric port address
before BGP starts.

## Announce the VIP

A routed VIP moves between control planes, and the speaker of the holder announces it. Set
`spec.vip.mode` to `routed` in the `Cluster`, and name an export interface as `spec.vip.interface`
in each control plane's `Node`:

```yaml title="nodes/cp1.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: controlplane
  nodeIP: {addresses: ["fdc8:a1b2:c3d4:ff::11"]}
  vip: {interface: node0}
```

chalkd adds the VIP to `node0` while the node holds it and removes it when another control plane
takes over, so FRR announces the route from the holder alone. `keepConfiguration: "yes"` in the
interface's `Network` keeps the VIP when networkd configures the interface again.

## Deliver and check the sessions

Validate the files and deliver them to each node, such as cp1:

```sh
chalkctl validate
chalkctl config apply cp1 --secrets=secrets.age
```

The console's Network tab lists the configured speaker's BGP sessions. Four diagnostics show
FRR's state: `bgp-summary` the sessions, `bgp-neighbor` one session in detail, `bfd-peers` the BFD
sessions and `bgp-route` the paths to a prefix. The output below comes from the node `k1`, which
peers with `r1` through the peer `r1` above and announces its address and the VIP from the dummy
interface `kube0`:

```console
$ chalkctl node diag k1 bgp-summary
IPv6 Unicast Summary:
BGP router identifier 0.0.0.31, local AS number 65031 VRF default vrf-id 0
BGP table version 5
RIB entries 3, using 480 bytes of memory
Peers 1, using 30 KiB of memory
Peer groups 1, using 72 bytes of memory
Neighbor        V         AS   MsgRcvd   MsgSent   TblVer  InQ OutQ  Up/Down State/PfxRcd   PfxSnt Desc
fd00:300::1     4      64512         7         4        5    0    0 00:00:03            1        2 N/A
Total number of neighbors 1
```

A session that is up shows the number of prefixes received, here r1's default route, and a
session that is down shows its state, such as `Active` or `Connect`. `PfxSnt` counts the
announced addresses: the node's own and the VIP. `bgp-neighbor` takes the neighbour's address or,
for an unnumbered session, the interface. Its output names the peer group, `peer-` and the
`BGPPeer`'s name, and shows whether the session has a password and BFD:

```console
$ chalkctl node diag k1 bgp-neighbor peer=fd00:300::1
BGP neighbor is fd00:300::1, remote AS 64512, local AS 65031, external link
 Member of peer-group peer-r1 for session parameters
  BGP state = Established, up for 00:00:03
...
Peer Authentication Enabled
...
  BFD: Type: single hop
  Detect Multiplier: 3, Min Rx interval: 300, Min Tx interval: 300
  Status: Up, Last update: 0:00:00:02
...
```

`bfd-peers` shows each BFD session and its timers:

```console
$ chalkctl node diag k1 bfd-peers
BFD Peers:
    peer fd00:300::1 local-address fd00:300::31 vrf default interface enp0s3
            ID: 1762522747
            Remote ID: 604162876
            Active mode
            Status: up
            Uptime: 2 second(s)
            Diagnostics: ok
            Remote diagnostics: ok
            Peer Type: dynamic
            RTT min/avg/max: 0/0/0 usec
            Local timers:
                    Detect-multiplier: 3
                    Receive interval: 300ms
                    Transmission interval: 300ms
                    Transmission interval (actual with jitter): 294ms
                    Detection timeout: 900ms
...
```

`bgp-route` shows the paths to a prefix, IPv6 by default and IPv4 with `family=ipv4`:

```console
$ chalkctl node diag k1 bgp-route prefix=::/0
BGP routing table entry for ::/0, version 5
Paths: (1 available, best #1, table default)
  Not advertised to any peer
  64512
    fd00:300::1 from fd00:300::1 (192.0.2.1)
    (fe80::5054:ff:fe00:201) (used)
      Origin IGP, metric 0, valid, external, best (First path received)
      Last update: Mon Nov 16 19:26:36 2026
```

`k1` keeps a default route from router advertisements on another interface, so this path stays in
FRR's table and the kernel's default route is `proto ra`, as
[Take the default route from the peers](#take-the-default-route-from-the-peers) describes.
`chalkctl node diag k1 routes family=inet6` shows which one the kernel holds.

The router shows the routes it learned. On a chalkos router, `chalkctl node routes` lists the
kernel's route to the VIP and the BGP path behind it:

```console
$ chalkctl node routes r1 fd00:301::10/128
DESTINATION       TYPE     PROTOCOL  TABLE  METRIC  NEXT HOPS
fd00:301::10/128  unicast  bgp       254    20      fe80::5054:ff:fe00:203 on enp0s3

BGP PREFIX        BEST  AS PATH  PEER          NEXT HOPS                             LOCAL PREF  MED  COMMUNITIES
fd00:301::10/128  *     65031    fd00:300::31  fd00:300::31, fe80::5054:ff:fe00:203  -           0    -
```

## Change the speaker on a running node

A delivery that changes the `BGPSpeaker`, a `BGPPeer` or a password reloads `frr.service`. This
dry run adds the dummy interface `lb0` to `k1`'s export interfaces:

```console
$ chalkctl config apply k1 --secrets=secrets.age --dry-run
k1: config 2 added, 0 removed, 1 changed
~ BGPSpeaker "bgp"
    spec.export.interfaces[1]: (none) → "lb0"
+ NetDev "20-lb0"
+ Network "20-lb0"
k1: config dd1465db… → 9a461558…
  outputs:  bgp, networkd
  effects:  bgp: reload frr.service (output bgp)
            kubernetes: restart chalkos-kubernetes.service kubelet.service (rule)
            networkd: reload systemd-networkd.service (output networkd)
  storage:  none
```

The effect `bgp` reloads FRR. The kubelet restarts because `lb0`'s static address changes the
node's addresses. The `BGPSpeaker`'s change takes FRR's reload alone.

The reload runs `chalkos-frr-reload`, which compares the new config with the one FRR runs and
applies the difference in place. Sessions the change leaves alone stay up. The journal of
`frr.service`, which `chalkctl logs k1 --unit=frr.service` shows, lists the commands it applied:

```text
k1 systemd[1]: Reloading frr.service - FRR's config and daemons...
k1 chalkos-frr-reload[2501]: chalkos-frr-reload: pass 1 applies:
k1 chalkos-frr-reload[2501]: hostname k1
k1 chalkos-frr-reload[2501]: route-map export permit 20
k1 chalkos-frr-reload[2501]:  match interface lb0
k1 chalkos-frr-reload[2501]: exit
k1 chalkos-frr-reload[2501]: chalkos-frr-reload: FRR runs frr.conf
k1 systemd[1]: Reloaded frr.service - FRR's config and daemons.
```

A reload that fails fails the delivery, with the config stored, and a restart of `frr.service`
loads the config whole.

A delivery that adds the node's first `BGPSpeaker`, or removes it, takes the effect `bgp-speaker`
too, which restarts `frr.service` in place of the reload: FRR starts with the speaker and stops
without it, which ends every session.
[A delivery reloads FRR](../how-it-works/router/bgp.md#a-delivery-reloads-frr)
explains the reload, which routers share.

## Fix a refused delivery

chalkctl and the node refuse a config that FRR could not run, before anything changes. The most
common refusals:

| Refusal | Cause |
| --- | --- |
| `node0 has no Network document whose [Match] Name= names it; FRR announces the addresses of interfaces the node configures` | An export interface has no `Network`. |
| `enp3s0 has no Network document whose [Match] Name= names it; FRR runs sessions on interfaces the node configures` | A peer's interface has no `Network`. |
| `the AS number stays 65001; to change it, deliver the config without the BGPSpeaker, ...` | The speaker's AS changed in place. |
| `the AS number stays 65000; add a BGPPeer of another name for 65003` | A peer's AS changed in place. |
| `the Node's nodeIP.addresses hold no IPv4 address, which FRR would take as its router ID; ...` | An IPv6-only node gives no router ID. |
| `a node with BGPPeer documents needs a BGPSpeaker document, ...` | The node has peers but no speaker. |
| `FRR binds a session to an address to one interface, ...` | A peer with an address names two interfaces. |
| `BGPPeer/router-enp1s0, BGPPeer/router-enp2s0: spec.address "fdc8:a1b2:c3d4:1::1" is not unique; FRR runs one session per neighbour address; ...` | Two peers name one address. |
| `fabric1 carries the unnumbered sessions of another BGPPeer too; ...` | Two peers run unnumbered sessions on one interface. |

To change the node's AS number, deliver the config without the `BGPSpeaker` and its `BGPPeer`s,
which ends the sessions, and then with the new number.

## Leave BGP out of an image

An image whose modules set
[`chalkos.kubernetes.bgp.enable`](../reference/image-options.md#chalkoskubernetesbgpenable) to
`false` carries no FRR. Its nodes refuse the kinds `BGPSpeaker` and `BGPPeer`, and it has no
outputs `bgp` and `bfd-firewall`, no effects `bgp` and `bgp-speaker`, and no diagnostics
`bgp-summary`, `bgp-neighbor`, `bgp-route` or `bfd-peers`. Its firewall leaves BFD's port closed.
Use such an image for nodes that need no BGP, or that run another routing daemon an extension
adds.
[Write a BGP extension](../guides/bgp-extension.md)
builds such an image with bird in place of FRR.

## Limits

- `chalkctl status` lists no Kubernetes BGP sessions. The console's Network tab and the
  diagnostics show them.
- The speaker announces the addresses of interfaces alone: no pod ranges, no LoadBalancer
  addresses and no routing policy beyond `import`.
- A peer's address must be on a subnet of one of the node's interfaces: eBGP runs with a TTL of
  1, without multihop.
- To carry IPv4 routes over a session to an IPv6 address, name the peer's interface. A session to
  an IPv6 address alone carries IPv6.
- FRR runs one session per neighbour address, and one unnumbered session per interface.
- On the interfaces of unnumbered and bound sessions, FRR's router advertisements carry a router
  lifetime of 0, so no host on the link takes the node as its default router.
- The node's firewall does not accept BGP's TCP port 179, so the node opens every session and
  the neighbour accepts it. A neighbour that only opens sessions itself never reaches the node.
- `BGPPeer` exists in `router.chalkos.dev` too, with other fields. An image carries one layer, so
  a node accepts one of the two.

## What next

- [Run a highly available control plane](ha-control-plane.md) sets up the routed VIP.
- [Kubernetes networking](../how-it-works/kubernetes/networking.md#the-bgp-speaker-announces-interfaces)
  explains how the VIP moves and what FRR announces.
- [Config kinds](../reference/kinds.md#bgpspeaker) lists every field of `BGPSpeaker` and
  [`BGPPeer`](../reference/kinds.md#bgppeer-kubernetes).
- [Build a home lab](../guides/home-lab.md) runs the speaker on a fabric of two ports per node.
