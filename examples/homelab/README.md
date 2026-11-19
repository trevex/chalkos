# A chalkos home lab

This example builds a home lab on four amd64 machines with two Ethernet ports each and one
unmanaged switch: a router on the home network and three Kubernetes control planes behind it, on
an IPv6 fabric. The router gives the fabric its addresses, a resolver with DNS64 and a NAT64 to the
IPv4 internet, and a WireGuard tunnel for the workstation. Each node announces its address over BGP
on both of its ports, so it stays reachable when one cable fails.

The guide [Build a home lab](https://trevex.github.io/chalkos/guides/home-lab/) takes you from
the bare machines to the running cluster: it shows how to find each machine's ports and disks,
adapt these files, install the machines and test a failover. Its source is
[`docs/guides/home-lab.md`](../../docs/guides/home-lab.md).

| File | Holds |
| --- | --- |
| `flake.nix` | Builds `router-metal`, `node-metal` and the installer. |
| `chalkos.yaml` | The inventory: the four nodes, the address chalkctl reaches each at, their images and files. |
| `router/router.yaml` | The router's ports, firewall, resolver, BGP and WireGuard. |
| `router/nat64.yaml` | The router's NAT64, the fabric's way to the IPv4 internet. |
| `router/secrets.yaml` | The router's WireGuard key and BGP password, as example values. |
| `cluster/cluster.yaml` | The Kubernetes cluster that all three nodes share. |
| `bgp/fabric.yaml`, `bgp/secrets.yaml` | The nodes' ports, their BGP sessions with the router, and the password. |
| `nodes/node1.yaml` to `nodes/node3.yaml` | Each node's disk, address and BGP speaker. |
| `chalkos-air-gapped.yaml`, `air-gapped/` | The optional variant without internet access: the inventory without the NAT64, the cluster registry and the router as the nodes' time server. |

The secrets in `router/secrets.yaml` and `bgp/secrets.yaml` are public example values. Replace them
before you use the files, as the guide describes.

The [BIRD extension example](../bgp-extension/) accompanies the separate extension guide.
