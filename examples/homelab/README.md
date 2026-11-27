# A chalkos home lab

This example builds a home lab of a router and up to three Kubernetes control planes behind it, on
an IPv6 fabric. It runs first as libvirt VMs on a Linux workstation, which `vm/vm.sh` creates, and
then on amd64 machines with two Ethernet ports each and an unmanaged switch: on machines, the
files change only in the ports' MAC addresses, the disks' serials and the router's address on the
home network. The router gives the fabric its addresses, a resolver with DNS64 and a NAT64 to the
IPv4 internet, and a WireGuard tunnel for the workstation. Each node announces its address over BGP
on both of its ports, so it stays reachable when one cable fails.

The guide [Build a home lab](https://trevex.github.io/chalkos/guides/home-lab/) takes you from
the empty VMs, or the bare machines, to the running cluster: it shows how to create the VMs, find
each machine's ports and disks, adapt these files, install the nodes and test a failover. Its
source is [`docs/guides/home-lab.md`](../../docs/guides/home-lab.md).

| File | Holds |
| --- | --- |
| `flake.nix` | Builds `router-metal`, `node-metal` and the installer. |
| `chalkos.yaml` | The inventory: the four nodes, the address chalkctl reaches each at, their images and files. |
| `router/router.yaml` | The router's ports, firewall, resolver, BGP and WireGuard. |
| `router/nat64.yaml` | The router's NAT64, the fabric's way to the IPv4 internet. |
| `router/secrets.yaml` | The router's WireGuard key and BGP password, as example values. |
| `cluster/cluster.yaml` | The Kubernetes cluster that all three nodes share. |
| `bgp/fabric.yaml`, `bgp/secrets.yaml` | The nodes' ports, their BGP sessions with the router, and the password. |
| `nodes/node1.yaml` to `nodes/node3.yaml` | Each node's ports, disk, address and BGP speaker. |
| `chalkos-air-gapped.yaml`, `air-gapped/` | The optional variant without internet access: the inventory without the NAT64, the cluster registry and the router as the nodes' time server. |
| `vm/vm.sh` | Creates the lab as libvirt VMs with the files' MACs and disk serials, plugs the installer in as a USB stick, pulls cables and deletes the lab. |

The secrets in `router/secrets.yaml` and `bgp/secrets.yaml` are public example values. Replace them
before you use the files, as the guide describes.

The [BIRD extension example](../bgp-extension/) accompanies the separate extension guide.
