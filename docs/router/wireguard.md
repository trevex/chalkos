---
title: "Run WireGuard"
description: "Give a router a WireGuard interface with keys from Secrets, add peers such as a laptop, let the tunnel through the firewall, rotate keys and check the handshakes"
---

# Run WireGuard

This page shows how to give a router a WireGuard interface, so that a laptop or another site
reaches the networks behind the router through an encrypted tunnel. The router runs the interface
with systemd-networkd. The private key and preshared keys stay in `Secret` documents: networkd
reads them as credentials, and no file that chalkos renders holds a key.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. Its
image must accept the WireGuard kinds. Router images do unless their variant sets the image option
[`chalkos.router.wireguard.enable`](../reference/image-options.md#chalkosrouterwireguardenable)
to `false`. An image without WireGuard refuses the kinds: `chalkctl validate` reports that it
`does not accept kind "WireGuard"`.

You also need `wg` from wireguard-tools on your workstation to make the keys.

The examples configure the router `r1`, which takes the address `203.0.113.1` on its WAN
interface `eth0` and routes the network `2001:db8:1::/64` on `eth1`. A laptop on the internet
reaches that network through the tunnel network `2001:db8:f0::/64`, in which the router has
`::1` and the laptop `::2`.

## Make the keys

Each end of a tunnel has a key pair. Make the router's and the laptop's, and a preshared key that
the two share on top of them:

```sh
wg genkey | tee r1.key | wg pubkey > r1.pub
wg genkey | tee laptop.key | wg pubkey > laptop.pub
wg genpsk > laptop.psk
```

`r1.key` goes into a `Secret` of the router's config, and `r1.pub` into the laptop's config.
`laptop.key` stays on the laptop, and `laptop.pub` goes into the router's `WireGuardPeer`. The
preshared key goes into both. Keep the private and preshared keys out of version control as you
keep the other secrets.

## Give the router a WireGuard interface

A `Secret` holds the router's private key and the laptop's preshared key, and a `WireGuard`
document creates the interface, named as the document:

```yaml
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: wireguard}
spec:
  data:
    private: <contents of r1.key>
    laptop: <contents of laptop.psk>
---
apiVersion: router.chalkos.dev/v1alpha1
kind: WireGuard
metadata: {name: wg0}
spec:
  addresses: ["2001:db8:f0::1/64"]
  privateKey: {secretRef: {name: wireguard, key: private}}
```

The interface receives its peers' packets on UDP port 51820 unless `listenPort` names another.
Two `WireGuard` documents take different ports. `addresses` gives the interface its addresses with
their prefix lengths, and the router routes each prefix into the tunnel. `mtu` sets the
interface's MTU from 1280 to 9000 bytes, 1420 when left out. `vrf` puts the interface into a
[VRF](vrfs.md), whose table then holds the routes to the peers; the tunnel's own UDP packets still
travel in the default VRF. A `VRF` document that names the interface in its `interfaces` is
refused: set the `WireGuard`'s `vrf` instead.

The document's name is the interface's, of 15 characters at most. It does not start with `n64-`
and is not the name of a `VRF`. The router renders the units `00-wg-wg0.netdev` and
`00-wg-wg0.network` for it. A `Network` document whose `Name=` names `wg0`, and one whose glob
matches `wg0` and sorts before `00-wg-wg0`, are refused. A glob of a `Network` that sorts after it
never configures the interface.

The `Secret`'s name and key hold letters, digits, `.`, `_` and `-` alone, as the names of
networkd's credentials do. The rendered `.netdev` names the key as
`PrivateKey=@secret_wireguard.private`, and networkd reads the value from its credentials when it
creates the interface. A value that is no key of 32 bytes in base64 leaves the interface without a private key, and networkd does not
create it; its journal says why.

## Add a peer

A `WireGuardPeer` names the interface, the peer's public key and the addresses the peer sends
from. `publicKey` holds the contents of `laptop.pub`:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: WireGuardPeer
metadata: {name: laptop}
spec:
  interface: wg0
  publicKey: TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=
  allowedIPs: ["2001:db8:f0::2/128"]
  presharedKey: {secretRef: {name: wireguard, key: laptop}}
```

`allowedIPs` works in both directions. The router accepts packets from the peer whose source lies
in one of the ranges, and routes each range to the interface in the table of the `WireGuard`'s
VRF, so it sends the peer the packets to them. A site behind a peer adds its networks to the
list, and the router routes them through the tunnel too:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: WireGuardPeer
metadata: {name: office}
spec:
  interface: wg0
  publicKey: +sR2gvS5qKqvh+jLYCQklPI9AQGcRYcDBHLBEJDZTn8=
  allowedIPs: ["2001:db8:f0::3/128", "2001:db8:5::/48"]
  endpoint: "office.example.org:51820"
  persistentKeepalive: 25
```

The ranges of two peers of one interface do not overlap, and two peers of one interface have
different public keys. A default route, `::/0` or `0.0.0.0/0`, is no range of a peer: the router
does not send its default traffic through a peer. The
[validator `router`](#fix-a-refused-delivery) refuses each of these.

A laptop behind NAT has no address the router can reach, so its peer has no `endpoint`: the
router waits for the laptop's first packet and answers where it came from. A peer the router
dials, such as another site, gives `endpoint` as `[<IPv6>]:<port>`, `<IPv4>:<port>` or
`<host name>:<port>`. `persistentKeepalive` sends a packet every that many seconds while the
tunnel is idle, which keeps the mapping of a NAT between the two open. Set it on the end behind
the NAT.

## Let the tunnel through the firewall

The firewall opens the UDP port of each `WireGuard` from anywhere by itself. The interface drops
every packet that no peer's key decrypts.

What comes out of the tunnel passes the firewall like the traffic of any interface. Put `wg0` into
a zone, and write the policies that let it reach the networks and the router:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallZone
metadata: {name: vpn}
spec: {interfaces: [wg0]}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallPolicy
metadata: {name: vpn-lan}
spec: {from: vpn, to: lan, default: accept}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallPolicy
metadata: {name: vpn-local}
spec:
  from: vpn
  to: local
  rules:
    - {protocol: udp, destinationPorts: [53], action: accept}
    - {protocol: tcp, destinationPorts: [53], action: accept}
```

[Filter and translate traffic](firewall-and-nat.md) describes zones and policies.

## Deliver and check the tunnel

Validate the files and deliver them:

```sh
chalkctl validate
chalkctl config apply r1 --secrets secrets.age
```

A delivery reloads networkd, which creates the interface, or applies the changed settings and
peers to an existing one. networkd sets every peer anew on each reload, whichever of its units
the delivery changed: a peer without `endpoint` loses the address the router learned and its
latest handshake, and the router reaches it again once it sends a packet. `persistentKeepalive` on
the peer's side keeps that wait short.

The diagnostic `wireguard` prints the interfaces as `wg show` does: the public keys, the
endpoints, the allowed IPs, the latest handshake and the bytes of each peer. It shows the
private and preshared keys as `(hidden)`. `interface` narrows it to one interface:

```sh
chalkctl node diag r1 --secrets secrets.age wireguard interface=wg0
```

```text
interface: wg0
  public key: uLRrWL5pg3FX2ydGsAsUrQ/VvWautg0OJpoJEGP12XM=
  private key: (hidden)
  listening port: 51820

peer: TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=
  preshared key: (hidden)
  endpoint: 198.51.100.7:41641
  allowed ips: 2001:db8:f0::2/128
  latest handshake: 12 seconds ago
  transfer: 9.11 KiB received, 12.40 KiB sent
```

A peer without `latest handshake` has not reached the router yet. The diagnostic needs a reader
client.

## Configure the laptop

The laptop's WireGuard config mirrors the router's documents. For wg-quick it reads:

```ini
[Interface]
PrivateKey = <contents of laptop.key>
Address = 2001:db8:f0::2/128

[Peer]
PublicKey = <contents of r1.pub>
PresharedKey = <contents of laptop.psk>
Endpoint = 203.0.113.1:51820
AllowedIPs = 2001:db8:f0::/64, 2001:db8:1::/64
PersistentKeepalive = 25
```

`Address` is the address of the laptop's peer in the router's `allowedIPs`. `Endpoint` is the
router's address and `listenPort`. `AllowedIPs` lists the networks the laptop sends through the
tunnel: the tunnel network and the networks behind the router. Bring the tunnel up with
`wg-quick up ./wg0.conf` and check it with `ping 2001:db8:f0::1`.

## Rotate a key

To rotate the router's private key, make a new key pair, put the new private key into the
`Secret` and deliver the config. The delivery reloads networkd, which reads its credentials
again and gives the interface the new key without recreating it. Give the peers the new public
key, as their tunnels break until they have it. A preshared key rotates the same way, on both
ends.

To rotate a peer's key, put its new public key into its `WireGuardPeer` and deliver the config.

## Remove an interface

A delivery without the `WireGuard` document removes its units, the firewall's opening of its
port and the interface itself, with its keys, peers and routes.

## Fix a refused delivery

The [validator `router`](../reference/builtins.md#validator-router) checks the peers of each
interface against each other and the interface against the `Network` and `VRF` documents.
`chalkctl validate` and `chalkctl config apply` run it before chalkctl sends anything:

| Message | Fix |
| --- | --- |
| `WireGuardPeer/office: spec.allowedIPs[0]: allowedIPs 2001:db8:f0::/64 overlaps 2001:db8:f0::2/128 of WireGuardPeer laptop on wg0; a WireGuard sends each address to one peer` | Narrow the ranges so that each address belongs to one peer. |
| `WireGuardPeer/spare: spec.publicKey: WireGuardPeer spare has the public key of WireGuardPeer laptop on wg0` | Drop one of the peers, or give it its own key. |
| `WireGuardPeer/office: spec.allowedIPs[1]: allowedIPs ::/0 is a default route, which the router does not send through a peer; name the ranges behind the peer` | List the networks behind the peer. |
| `WireGuardPeer/office: spec.endpoint: endpoint "office..example.org:51820" is none of [<IPv6>]:<port>, <IPv4>:<port> and <host name>:<port> with a port from 1 to 65535` | Give an address or a host name and a port. |
| `Network/50-wg0: spec.match: Network 50-wg0 names wg0, the interface of WireGuard wg0, whose own unit configures it; drop wg0 from the Network` | Move its settings to the `WireGuard` document, and drop `wg0` from the `Network`. |
| `Network/00-all: metadata.name: Network 00-all matches the link of WireGuard wg0 before 00-wg-wg0; rename it to sort after 00-wg-wg0` | Rename the `Network` document. |
| `WireGuard/wg0: spec.vrf: VRF lab holds wg0 by its wg*, but the WireGuard is in VRF default; give the WireGuard vrf lab, or keep its link out of VRF lab's interfaces` | Put the interface into a VRF with the `WireGuard`'s `vrf` alone, and leave it out of the `VRF`'s `interfaces`. |
| `VRF/lab: spec.interfaces[0]: VRF lab names wg0, the interface of WireGuard wg0; set the WireGuard's vrf instead` | Drop `wg0` from the `VRF` and set `vrf: lab` in the `WireGuard`. |

## What next

- [Config kinds](../reference/kinds.md#wireguard) describes each field of `WireGuard` and
  `WireGuardPeer`.
- [Route with BGP](bgp.md) peers with another site over the tunnel.
- [Separate routing with VRFs](vrfs.md) puts the tunnel into a VRF of its own.
