---
title: "How chalkos works"
description: "How chalkos works and why it works that way"
---

# How chalkos works

These pages are for operators, image authors and contributors who want the mechanism behind a
task. They explain how a node is built, configured, booted, secured and upgraded, and where each
protection stops. For the whole model on one page first, read
[What is chalkos](../get-started/what-is-chalkos.md).

## Read in this order

1. [How a node works](node/overview.md) follows a node from build to upgrade.
2. [The image](node/image.md) describes what a node boots.
3. [Config and the inventory](node/config.md) describes how a node takes its YAML.
4. [Chain of trust](node/trust.md) follows trust from the firmware to the API.
5. [Security model](node/security.md) lists what each protection stops and where it ends.

## The node OS

These pages describe the operating system of a node: the base that every image carries. A layer, a
predefined set of NixOS modules that chalkos ships and an image lists in its modules, adds one role
on top of the base, and an image carries one layer at most. The Kubernetes layer and the router
layer below describe what chalkos adds to run Kubernetes on it, or to route between networks.

<!-- docgen cards: node/overview.md node/architecture.md node/image.md node/boot-and-rollback.md node/config.md node/config-pipeline.md node/storage.md node/networking.md node/trust.md node/security.md node/certificates.md node/upgrades.md -->
<div class="grid cards" markdown>

-   __[How a node works](node/overview.md)__

    ---

    The machines that build, drive and run a chalkos node, and the stages of a node's life

-   __[Architecture](node/architecture.md)__

    ---

    The parts of chalkos, where each one runs, and how chalkd picks its mode

-   __[The image](node/image.md)__

    ---

    What an image holds: the erofs store on dm-verity, the signed UKI and the A/B slots

-   __[Boot, health and rollback](node/boot-and-rollback.md)__

    ---

    How a node boots, decides whether a boot is healthy, and falls back from an image that is not

-   __[Config and the inventory](node/config.md)__

    ---

    How YAML config documents describe each node, how the inventory gives them out and how a node applies them

-   __[The config pipeline](node/config-pipeline.md)__

    ---

    How a delivery passes a node's documents through rules, outputs and effects, at boot and after an upgrade

-   __[Storage and encryption](node/storage.md)__

    ---

    STATE, VAR and volumes, how they are encrypted and unlocked, and how they change after install

-   __[Networking](node/networking.md)__

    ---

    How a node configures its interfaces with systemd-networkd, filters traffic with nftables and keeps its clock

-   __[Chain of trust](node/trust.md)__

    ---

    How trust passes from the firmware to the node API, where each key lives, and what no one can verify yet

-   __[Security model](node/security.md)__

    ---

    What Secure Boot, the TPM, the OS CA and client roles protect, and what they do not

-   __[Certificates](node/certificates.md)__

    ---

    The OS CA, the node CA and the certificates of the node API, how they are renewed and rotated, and why the clock matters

-   __[Upgrades](node/upgrades.md)__

    ---

    How a new image reaches a node, how the node writes and checks it, and how it boots it on trial

</div>
<!-- end docgen cards -->

## Kubernetes layer

These pages describe what chalkos adds to a node to run Kubernetes on images with the
Kubernetes layer.

<!-- docgen cards: kubernetes/overview.md kubernetes/control-plane.md kubernetes/networking.md kubernetes/certificates.md kubernetes/upgrades.md -->
<div class="grid cards" markdown>

-   __[How Kubernetes runs on a node](kubernetes/overview.md)__

    ---

    What a Kubernetes node adds to the node OS: its components by node type, its share and its config

-   __[The control plane](kubernetes/control-plane.md)__

    ---

    How chalkd runs etcd and the control plane as static pods, starts the cluster, joins control planes and applies add-ons

-   __[Kubernetes networking](kubernetes/networking.md)__

    ---

    How a Kubernetes node picks its addresses, how dual stack, the VIP and flannel work, and which ports the control plane opens

-   __[Kubernetes certificates](kubernetes/certificates.md)__

    ---

    The Kubernetes CAs and keys, the certificates a control plane and the kubelets run on, how the CAs and keys are rotated, and how a node is revoked

-   __[Kubernetes upgrades](kubernetes/upgrades.md)__

    ---

    How a new Kubernetes release reaches a cluster: the control plane through config, then the kubelets in images that roll through the control planes first

</div>
<!-- end docgen cards -->

## Router layer

These pages describe what chalkos adds to a node of a router image to forward between its
networks and filter what passes.

<!-- docgen cards: router/overview.md router/firewall.md router/bgp.md router/vrfs.md router/policy-routing.md router/nat64-and-dns.md router/dhcp.md -->
<div class="grid cards" markdown>

-   __[How a router works](router/overview.md)__

    ---

    What the router layer adds to a node: the router kinds, forwarding set by a Router document, the firewall, policy routing, BGP, NAT64, the resolver, the boot order and the management guard

-   __[The router's firewall](router/firewall.md)__

    ---

    The nftables table of a router image: the protected rules, chalkd's port, zones and policies, NAT, zones of VRFs, conntrack zones, the reverse-path filter and the fallback ruleset

-   __[BGP on a router](router/bgp.md)__

    ---

    How a router image runs BGP: FRR, the config the output frr renders from the BGP kinds, conditional default routes, the passwords, the reload, the boot order, the firewall's ports and the state chalkd reads

-   __[VRFs on a router](router/vrfs.md)__

    ---

    How a router image runs VRFs: management in the default VRF, the units networkd gets, the unreachable defaults, chalkos-vrf, FRR's instances, the firewall, NAT64s and resolvers in VRFs, and chalkd

-   __[Policy routing on a router](router/policy-routing.md)__

    ---

    How a router image routes by policy: the firewall's marks, the chain after the reverse-path filter, networkd's rules at priority 900 and the next hops' tables that fail closed

-   __[NAT64 and DNS64 on a router](router/nat64-and-dns.md)__

    ---

    How a router image runs NAT64 with Jool and its resolvers with Unbound: the rendered instances, links and configs, each NAT64's network namespace, the firewall around it, the resolvers of VRFs, the reloads and the state chalkd reads

-   __[DHCP on a router](router/dhcp.md)__

    ---

    How a router image leases addresses with Kea and relays DHCP with dnsmasq: the configs the outputs render per VRF and family, the sockets and the firewall, chalkos-dhcp and the servers of each VRF, the routes of delegated prefixes, the relays, the lease files and the state chalkd reads

</div>
<!-- end docgen cards -->
