---
title: "Run a router"
description: "Set up chalkos nodes as routers: forwarding, the node API's interfaces, the firewall, NAT, BGP, VRFs, policy routing, NAT64 and DNS"
---

# Run a router

These pages are for operators who run nodes of a router image, one whose variant sets
`router = true`. They build on [Operate nodes](../operate/index.md): a router installs, upgrades
and recovers like any node, and its interfaces and addresses are `Network` documents as on any
node. A router's own documents, of `router.chalkos.dev`, set how it forwards, what its firewall
lets through, whom it exchanges routes with over BGP, which links share a routing table, how it
translates IPv6 to IPv4 and resolves names, and which addresses it leases.

<div class="grid cards" markdown>

-   :material-router-network: __Set up a router__

    ---

    Write the `Router` document, choose the interfaces of the node API and check what the router
    loaded.

    [:octicons-arrow-right-24: Read](set-up.md)

-   :material-wall-fire: __Filter and translate traffic__

    ---

    Group interfaces into zones, write policies between them and to the router, and masquerade or
    forward ports.

    [:octicons-arrow-right-24: Read](firewall-and-nat.md)

-   :material-transit-connection-variant: __Route with BGP__

    ---

    Give the router a BGP instance, peer on its links or at addresses, filter routes with
    policies and check the sessions.

    [:octicons-arrow-right-24: Read](bgp.md)

-   :material-call-split: __Separate routing with VRFs__

    ---

    Put links into VRFs with tables of their own, run a BGP instance in each and import routes
    between them, while the node API stays in the default VRF.

    [:octicons-arrow-right-24: Read](vrfs.md)

-   :material-sign-direction: __Route traffic by policy__

    ---

    Send traffic by source, port or DSCP into a VRF, another table or through another router,
    whatever its destination's route.

    [:octicons-arrow-right-24: Read](policy-routing.md)

-   :material-swap-horizontal: __Translate to IPv4 and resolve names__

    ---

    Give IPv6-only networks the IPv4 internet through NAT64, answer their queries with DNS64 and
    local zones, and check the translations.

    [:octicons-arrow-right-24: Read](nat64-and-dns.md)

-   :material-ip-network: __Lease addresses with DHCP__

    ---

    Lease IPv4 addresses on the router's links from pools and reservations, give clients their
    options and PXE clients their boot files, in each VRF.

    [:octicons-arrow-right-24: Read](dhcp.md)

</div>
