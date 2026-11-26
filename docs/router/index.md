---
title: "Run a router"
description: "Set up chalkos nodes as routers: forwarding, the node API's interfaces, the firewall, NAT, BGP, VRFs, policy routing, NAT64, DNS, DHCP, WireGuard and NTP"
---

# Run a router

These pages are for operators who run nodes of a router image, one whose modules list the router
layer, `chalkos.nixosModules.router`. They build on [Operate nodes](../operate/index.md): a router
installs, upgrades and recovers like any node, and its interfaces and addresses are `Network`
documents as on any node. A router's own documents, of `router.chalkos.dev`, set how it forwards,
what its firewall lets through, whom it exchanges routes with over BGP, which links share a routing
table, how it translates IPv6 to IPv4 and resolves names, which addresses it leases, which peers
reach it through WireGuard, and whom it serves time.

To see a router run first, [Quick start: router](../get-started/quick-start-router.md) runs one
with a client on its LAN as virtual machines, in about 15 minutes.

## Read in this order

1. [Set up a router](set-up.md) gives a node its router config.
2. [Filter and translate traffic](firewall-and-nat.md) decides what passes.
3. [Route with BGP](bgp.md) exchanges routes with the router's neighbours.
4. [Lease addresses with DHCP](dhcp.md) leases addresses on the router's networks.

## Set up

These pages give a node its router config and its firewall.

<!-- docgen cards: set-up.md firewall-and-nat.md -->
<div class="grid cards" markdown>

-   __[Set up a router](set-up.md)__

    ---

    Configure a router node's interfaces, its Router document and the interfaces of its node API, deliver the config and check the result

-   __[Filter and translate traffic](firewall-and-nat.md)__

    ---

    Group a router's interfaces into zones, write firewall policies between them and to the router, and masquerade or forward ports with NAT rules

</div>
<!-- end docgen cards -->

## Route

These pages decide where the router sends traffic.

<!-- docgen cards: bgp.md vrfs.md policy-routing.md -->
<div class="grid cards" markdown>

-   __[Route with BGP](bgp.md)__

    ---

    Give a router a BGP instance, peer with its neighbours on interfaces, at addresses or from a prefix, filter routes with prefix lists and routing policies, and check its sessions and routes

-   __[Separate routing with VRFs](vrfs.md)__

    ---

    Put a router's links into VRFs, run a BGP instance in each and import routes between them, while the node API stays in the default VRF

-   __[Route traffic by policy](policy-routing.md)__

    ---

    Send a router's forwarded traffic by source, port or DSCP into a VRF, another table or through a next hop, whatever its destination's route

</div>
<!-- end docgen cards -->

## Serve the networks

These pages give the router's networks their services.

<!-- docgen cards: nat64-and-dns.md dhcp.md wireguard.md time-server.md -->
<div class="grid cards" markdown>

-   __[Translate to IPv4 and resolve names](nat64-and-dns.md)__

    ---

    Give IPv6-only networks the IPv4 internet through a router's NAT64, answer their DNS queries with DNS64 and local zones, do both in a VRF, and check the translations and the resolvers

-   __[Lease addresses with DHCP](dhcp.md)__

    ---

    Serve IPv4 and IPv6 addresses on a router's links with Kea, delegate IPv6 prefixes to other routers and relay DHCP to servers elsewhere: pools, reservations, options, PXE boot files and a server per VRF

-   __[Run WireGuard](wireguard.md)__

    ---

    Give a router a WireGuard interface with keys from Secrets, add peers such as a laptop, let the tunnel through the firewall, rotate keys and check the handshakes

-   __[Serve time with NTP](time-server.md)__

    ---

    Let a router answer NTP for its networks with a TimeServer, point nodes at it, serve the router's own clock without a source, and check its clients

</div>
<!-- end docgen cards -->
