---
title: "Quick start: router"
description: "Run a chalkos router and a client on its LAN as QEMU virtual machines with chalklab, and change its firewall on trial"
---

# Quick start: router

This tutorial runs a chalkos router and a client machine behind it on your machine. The router
takes its WAN address by DHCP, leases an address to the client, answers its DNS queries and
masquerades its traffic to the internet. You check each of these from the client, then change the
router's firewall on trial and remove the lab again. chalklab runs both machines as QEMU virtual
machines with UEFI firmware, Secure Boot and a TPM. Both machines run the router image that each
chalkos release publishes for virtual machines, which chalklab pulls from the registry. It takes
about 15 minutes.

## Before you begin

You need:

- an x86-64 Linux machine with [Nix](https://nixos.org/download/) and flakes enabled;
- read and write access to `/dev/kvm`, which most distributions give to members of the `kvm`
  group;
- about 2 GiB of free memory, 1 GiB for the router and 768 MiB for the client, and about 10 GiB
  of free disk for `/nix/store` and the lab;
- git.

You do not need root. Nix gives you chalklab and chalkctl from the `router-lab` template's
development shell; the tutorial builds no image.

## What the lab runs

Each virtual machine has two network cards. One is on QEMU's user-mode network, which reaches
the internet through your machine and carries the port that chalklab forwards to each node's
chalkd. The other is on the lab network, a switch between the two machines. The router's card on
the user-mode network is its WAN, and its card on the lab network is its LAN:

```mermaid
flowchart LR
    internet["internet, through your machine"]
    chalkctl["chalkctl on your machine"]
    router["router: DHCP server, resolver, firewall, masquerade"]
    client["client"]
    internet ---|"enp0s2, WAN by DHCP"| router
    router ---|"enp0s3, LAN 192.168.50.1/24"| client
    chalkctl -->|"forwarded port to 50000"| router
    chalkctl -->|"forwarded port to 50000"| client
```

The client's card on the user-mode network has an address and no gateway. It carries chalkctl's
connections alone, so the client reaches the internet only through the router.

## Create the lab's files

Create a directory and initialise it from the `router-lab` template:

<!-- x-release-please-start-version -->
```console
$ mkdir router-lab && cd router-lab
$ nix flake init -t github:trevex/chalkos/v0.10.0#router-lab
wrote: "/home/alice/router-lab/.gitignore"
wrote: "/home/alice/router-lab/README.md"
wrote: "/home/alice/router-lab/chalklab.yaml"
wrote: "/home/alice/router-lab/chalkos.yaml"
wrote: "/home/alice/router-lab/flake.nix"
wrote: "/home/alice/router-lab/nodes/client.yaml"
wrote: "/home/alice/router-lab/nodes/router.yaml"
wrote: "/home/alice/router-lab/nodes"
```
<!-- x-release-please-end -->

The files:

| File | Holds |
| --- | --- |
| `chalkos.yaml` | The inventory: the nodes `router` and `client`, the image each runs and its file. |
| `flake.nix` | The development shell with the tools, and images of your own: `router-kvm`, with the router layer, and `client-kvm`, without a layer. |
| `nodes/router.yaml` | The router's interfaces, firewall, NAT, DHCP server and resolver. |
| `nodes/client.yaml` | The client's interfaces. |
| `chalklab.yaml` | The lab file: the nodes chalklab runs, with their memory and MAC addresses. |

Both nodes run the published router image, `ghcr.io/trevex/chalkos/router-kvm`, named by the tag
of the template's release. The client gets no router documents, so it runs none of the router's
services:

```yaml title="chalkos.yaml"
--8<-- "templates/router-lab/chalkos.yaml"
```

The router's file holds every document of its role. The firewall accepts the node API on the WAN
alone, where chalklab forwards its port:

```yaml title="nodes/router.yaml"
--8<-- "templates/router-lab/nodes/router.yaml"
```

Nix evaluates a flake from the files git tracks, so put the files under git:

```sh
git init && git add .
```

## Generate the secrets and check the config

Enter the flake's development shell, which has chalklab and chalkctl of the chalkos version the
flake pins. Run every later command of this tutorial in it:

```sh
nix develop
```

Generate the cluster's secrets with
[`chalkctl gen secrets`](../reference/cli/chalkctl_gen_secrets.md):

```console
$ chalkctl gen secrets --plaintext
wrote secrets.json and secrets.pub.json
warning: secrets.json holds the cluster's secrets unencrypted; protect it by other means, for example keep it out of version control with a .gitignore entry
```

The published image carries no OS CA, so until chalklab installs a node, the node accepts any
client in maintenance mode. That is fine on the lab's private network. The images `flake.nix`
builds name `secrets.pub.json`, the secrets' public part, so they trust the cluster's OS CA.

The template's `.gitignore` keeps `secrets.json` out of git. Commit the files with `flake.lock`,
then check the nodes' config against the image with
[`chalkctl validate`](../reference/cli/chalkctl_validate.md). It reads the image's contract from
the registry:

```console
$ git add . && git commit -m "A chalkos router lab"
$ chalkctl validate
client: valid for the image router-kvm at ghcr.io/trevex/chalkos/router-kvm@sha256:150c8610ee376be69a88dbae719950253c82f67a701c76f265111e9b4e7edaeb (router-kvm on kvm, root hash b5558bdbabdd), config 94f1f34ad29ff7bc1f152158df4504bbf4576fc6d9c230a5ee9fd77bed47dc10
router: valid for the image router-kvm at ghcr.io/trevex/chalkos/router-kvm@sha256:150c8610ee376be69a88dbae719950253c82f67a701c76f265111e9b4e7edaeb (router-kvm on kvm, root hash b5558bdbabdd), config e3befdf104b789da4dd4f9c58b263905755e3428e7b9e9d560a75552e161b87e
```

## Create the lab

[`chalklab create`](../reference/cli/chalklab_create.md) pulls the image once for both nodes,
starts both virtual machines and installs each node. Neither runs Kubernetes, so there is no
bootstrap:

<!-- x-release-please-start-version -->
```console
$ chalklab create --secrets=secrets.json
creating the lab's Secure Boot keys in /home/alice/.local/state/chalklab/router-lab/keys
pulled ghcr.io/trevex/chalkos/router-kvm:v0.10.0 as ghcr.io/trevex/chalkos/router-kvm@sha256:<digest>
copying the image of client from /home/alice/.local/state/chalklab/router-lab/images/sha256-<digest>
using cached ghcr.io/trevex/chalkos/router-kvm@sha256:<digest>
copying the image of router from /home/alice/.local/state/chalklab/router-lab/images/sha256-<digest>
starting client, router
wrote /home/alice/.local/state/chalklab/router-lab/chalkctl.json for chalklab with the admin role; its certificate expires on 2027-11-20
waiting for client to come up in maintenance mode
client runs in maintenance mode with the certificate fingerprint c54072ed12d5de6cbc33426e7d6e0bc38692b815048e2cee58debae55a424ec7
installing client in place
client is installed and reboots
chalkctl recovery-key client prints the key that unlocks it when its TPM fails
waiting for router to come up in maintenance mode
router runs in maintenance mode with the certificate fingerprint 23d801060185d696ce9d05a3207fea362df93c72e48d032fc046835db822c09a
installing router in place
router is installed and reboots
chalkctl recovery-key router prints the key that unlocks it when its TPM fails

the lab router-lab runs client, router; next:
  export CHALKOSCONFIG=/home/alice/.local/state/chalklab/router-lab/chalkctl.json
  export CHALKOSINVENTORY=/home/alice/.local/state/chalklab/router-lab/chalkos.yaml
  chalkctl status client
  chalklab status
  chalklab destroy
The lab's inventory names unsigned images, which its firmware refuses; upgrade with a copy
signed by the lab's db key:
  chalklab sign <image dir> --out <signed dir>
  chalkctl upgrade --image <signed dir>
```
<!-- x-release-please-end -->

chalklab installs the nodes in the order of their names, so the client comes first. It takes its
LAN address once the router runs. Export the client file and the lab's inventory, as the output
says. chalkctl then reaches both nodes at their forwarded ports without further flags:

```sh
export CHALKOSCONFIG=~/.local/state/chalklab/router-lab/chalkctl.json
export CHALKOSINVENTORY=~/.local/state/chalklab/router-lab/chalkos.yaml
```

## Check the router

[`chalkctl status`](../reference/cli/chalkctl_status.md) shows what the router runs, and the state
of its router services last:

```console
$ chalkctl status router
router: healthy
config e3befdf104b789da4dd4f9c58b263905755e3428e7b9e9d560a75552e161b87e (the inventory's)
platform kvm (the inventory's)
image b5558bdbabdd, booted from chalkos_1.efi
VOLUME  DISK    MOUNT POINT  STATE
var     system  /var         mounted
OUTPUT           HASH          STATE
chrony           0af9b0bd41ca  rendered
chrony-server    e3b0c44298fc  rendered
dhcp-relay       e3b0c44298fc  rendered
frr              956667f45766  rendered
jool             e3b0c44298fc  rendered
kea              4a4da68c9a2f  rendered
networkd         8b35eed0386f  rendered
router-nftables  26bad37289d7  rendered
router-sysctl    469f31935ae7  rendered
unbound          23d0b4df374f  rendered
bgp: no sessions
resolver: running
dhcp4 default: running
  SUBNET  RANGE            LEASED  POOL
  lan     192.168.50.0/24  1       100
certificates:
  node   expires 2027-11-20
  OS CA  expires 2036-11-17
trust:
  OS CA  d48aae5be11ac0a7
time: synchronised to 192.53.103.108, offset -0.000000 s
```

Each output is a file the router renders from its config, such as `router-nftables` for the
firewall and `kea` for the DHCP server. The router runs no BGP, so it has no sessions. Its
resolver runs, and its DHCP server has leased one address of the subnet `lan`.
[`chalkctl node diag`](../reference/cli/chalkctl_node_diag.md) runs the diagnostic
`dhcp-leases`, which shows the lease:

```console
$ chalkctl node diag router dhcp-leases
VRF	SUBNET	ADDRESS	MAC	HOSTNAME	ENDS
default	lan	192.168.50.100	52:54:00:7c:00:02	client	2026-11-17T00:34:51Z
```

The client took the first address of the pool. Its MAC address is the one `chalklab.yaml` gives
its card on the lab network.

## Check the client

The diagnostic `route-get` shows the route the client takes to an address on the internet. It goes
through the router's LAN address, the gateway the DHCP server gave:

```console
$ chalkctl node diag client route-get address=1.1.1.1
[{"dst":"1.1.1.1","gateway":"192.168.50.1","dev":"enp0s3","prefsrc":"192.168.50.100","flags":[],"uid":63250,"cache":[]}]
```

`tcp-connect` opens a connection along that route. The firewall's policy `lan-wan` lets it out,
and the router masquerades it behind its WAN address:

```console
$ chalkctl node diag client tcp-connect address=1.1.1.1 port=443
connected to 1.1.1.1 port 443
```

`dns-query` asks the router's resolver for a name:

```console
$ chalkctl node diag client dns-query name=example.org type=A server=192.168.50.1
...
;; ANSWER SECTION:
example.org.	294	IN	A	104.20.26.136
example.org.	294	IN	A	172.66.157.237
...
;; SERVER: 192.168.50.1
...
```

## Open the node API to the LAN on trial

The router's firewall drops connections from the LAN to the router, except DHCP, DNS and the
traffic chalkos itself accepts. The node API on port 50000 is not among them:

```console
$ chalkctl node diag client tcp-connect address=192.168.50.1 port=50000
no connection to 192.168.50.1 port 50000 within 5 seconds
chalkctl: tcp-connect exited with status 124
```

Let the LAN reach the node API too. Add the LAN's card to the management interfaces in
`nodes/router.yaml`:

```yaml title="nodes/router.yaml"
apiVersion: router.chalkos.dev/v1alpha1
kind: Router
metadata: {name: router}
spec:
  management: {interfaces: [enp0s2, enp0s3]}
```

Validate the files and preview the change with
[`chalkctl config apply`](../reference/cli/chalkctl_config_apply.md) and `--diff`, which changes
nothing. Delivering config needs the secrets file:

```console
$ chalkctl validate
client: valid for the image router-kvm at ghcr.io/trevex/chalkos/router-kvm@sha256:150c8610ee376be69a88dbae719950253c82f67a701c76f265111e9b4e7edaeb (router-kvm on kvm, root hash b5558bdbabdd), config 94f1f34ad29ff7bc1f152158df4504bbf4576fc6d9c230a5ee9fd77bed47dc10
router: valid for the image router-kvm at ghcr.io/trevex/chalkos/router-kvm@sha256:150c8610ee376be69a88dbae719950253c82f67a701c76f265111e9b4e7edaeb (router-kvm on kvm, root hash b5558bdbabdd), config 891df0f1c1918a7506943eaa311ac4269b46821338424da6e4122fe2ea7cab26
$ chalkctl config apply router --secrets=secrets.json --diff
router: config 0 added, 0 removed, 1 changed
~ Router "router"
    spec.management.interfaces[1]: (none) → "enp0s3"
```

A wrong management interface cuts chalkctl off from the router. Deliver the change with `--try`,
so that the router rolls it back by itself unless chalkctl reaches it again and confirms it:

```console
$ chalkctl config apply router --secrets=secrets.json --try 2m
router: config 0 added, 0 removed, 1 changed
router: delivering config 891df0f1c1918a7506943eaa311ac4269b46821338424da6e4122fe2ea7cab26 on trial until 2026-11-16T12:37:41Z
router runs config 891df0f1c1918a7506943eaa311ac4269b46821338424da6e4122fe2ea7cab26 on trial; it rolls back to config e3befdf104b789da4dd4f9c58b263905755e3428e7b9e9d560a75552e161b87e by itself unless confirmed within 2m0s
router: waiting for a new connection and the node's health to confirm config 891df0f1c1918a7506943eaa311ac4269b46821338424da6e4122fe2ea7cab26
router keeps config 891df0f1c1918a7506943eaa311ac4269b46821338424da6e4122fe2ea7cab26: the trial is confirmed
```

chalkctl reached the router over a new connection on its WAN, so it confirmed the trial. The
router keeps the new config, and its firewall now accepts the node API from the LAN:

```console
$ chalkctl node diag client tcp-connect address=192.168.50.1 port=50000
connected to 192.168.50.1 port 50000
```

`chalkctl status router` now names the new config as the inventory's, and the config before it as
the one `chalkctl config rollback` restores.

## Remove the lab

[`chalklab destroy`](../reference/cli/chalklab_destroy.md) stops the virtual machines and removes
the lab's state, its disks, keys and the pulled image included:

```console
$ chalklab destroy
removed the lab in /home/alice/.local/state/chalklab/router-lab
```

Clear the variables, which name the removed lab's files:

```sh
unset CHALKOSCONFIG CHALKOSINVENTORY
```

The files stay, so `chalklab create --secrets=secrets.json` makes the same lab again, and pulls
the image once more.

## What next

The lab ran on the published router image. To run a router of your own on real machines with it,
name `router-metal` from [Releases](releases.md) in the inventory. To add anything to the image,
such as a package, an extension or an option, build your own: the template's `README.md` shows
how to point the inventory at the `router-kvm` and `client-kvm` that `flake.nix` builds, and
[Your first image flake](../build/first-image.md) starts the image author's pages.
[Published images or your own](decisions.md#published-images-or-your-own) compares the two paths.

- [Set up a router](../router/set-up.md) covers the `Router` document and the node API's
  interfaces on a router of real machines.
- [Filter and translate traffic](../router/firewall-and-nat.md) explains zones, policies and NAT
  rules, and [Lease addresses with DHCP](../router/dhcp.md) the DHCP server.
- [Try a config change](../operate/change-config.md#try-a-config-change) explains trials and
  how to confirm one later.
- [The router's firewall](../how-it-works/router/firewall.md) shows the chains the router loads.
- [Quick start](quick-start-kubernetes.md) runs a Kubernetes cluster in the same way.
