---
title: "Run a router"
description: "Set up chalkos nodes as routers: forwarding, the node API's interfaces, the firewall and NAT"
---

# Run a router

These pages are for operators who run nodes of a router image, one whose variant sets
`router = true`. They build on [Operate nodes](../operate/index.md): a router installs, upgrades
and recovers like any node, and its interfaces and addresses are `Network` documents as on any
node. A router's own documents, of `router.chalkos.dev`, set how it forwards and what its firewall
lets through.

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

</div>
